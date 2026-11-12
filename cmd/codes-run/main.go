// codes-run runs one CodeS conversation without a terminal UI.
package main

import (
	"CodeS/internal/tools/manager"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	exitOK        = 0
	exitFailed    = 1
	exitArguments = 2
	exitStopped   = 3
	exitTimeout   = 124
	exitCanceled  = 130
)

type options struct {
	workspace, config, provider, model string
	prompt, promptFile, executionMode  string
	runID                              string
	maxTurns                           int
	timeout, heartbeat                 time.Duration
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func parseOptions(args []string) (options, error) {
	o := options{}
	f := flag.NewFlagSet("codes-run", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	f.StringVar(&o.workspace, "workspace", ".", "workspace directory")
	f.StringVar(&o.config, "config", "config.yaml", "configuration file (relative to launch directory)")
	f.StringVar(&o.provider, "provider", "", "provider name from configuration (required)")
	f.StringVar(&o.model, "model", "", "model ID (required)")
	f.StringVar(&o.prompt, "prompt", "", "task text; mutually exclusive with --prompt-file")
	f.StringVar(&o.promptFile, "prompt-file", "", "UTF-8 task file, or - to read stdin until EOF")
	f.StringVar(&o.executionMode, "execution-mode", "", "manual or full_access; defaults to configuration (manual never prompts)")
	f.StringVar(&o.runID, "run-id", "", "task identifier for every event (generated if omitted)")
	f.IntVar(&o.maxTurns, "max-turns", 100, "maximum Agent turns")
	f.DurationVar(&o.timeout, "timeout", 30*time.Minute, "total timeout including startup; 0 disables")
	f.DurationVar(&o.heartbeat, "heartbeat-interval", 10*time.Second, "heartbeat interval; 0 disables")
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: codes-run --provider NAME --model ID (--prompt TEXT | --prompt-file PATH) [options]")
		fmt.Fprintln(f.Output(), "stdout: JSONL events; stderr: diagnostics. No TUI or interactive approval.")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments; pass the task with --prompt or --prompt-file")
	}
	if strings.TrimSpace(o.provider) == "" || strings.TrimSpace(o.model) == "" {
		return o, errors.New("--provider and --model are required")
	}
	var hasPrompt, hasFile bool
	f.Visit(func(value *flag.Flag) {
		hasPrompt = hasPrompt || value.Name == "prompt"
		hasFile = hasFile || value.Name == "prompt-file"
	})
	if hasPrompt == hasFile || (hasPrompt && strings.TrimSpace(o.prompt) == "") || (hasFile && o.promptFile == "") {
		return o, errors.New("provide exactly one nonempty --prompt or --prompt-file (use --prompt-file - for stdin)")
	}
	if !utf8.ValidString(o.prompt) {
		return o, errors.New("--prompt must be UTF-8")
	}
	if o.maxTurns <= 0 || o.timeout < 0 || o.heartbeat < 0 {
		return o, errors.New("--max-turns must be positive; durations must not be negative")
	}
	if o.executionMode != "" {
		if err := manager.ExecutionMode(o.executionMode).Validate(); err != nil {
			return o, err
		}
	}
	return o, nil
}

func run(args []string) int {
	o, err := parseOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if o.runID == "" {
		o.runID = rand.Text()
	}
	output := newEventWriter(os.Stdout, o.runID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "codes-run:", err)
		_ = output.write(event{"event": "failed", "phase": "arguments", "error": err.Error(), "exit_code": exitArguments})
		return exitArguments
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	if o.timeout > 0 {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithTimeout(ctx, o.timeout)
		defer deadlineCancel()
	}
	if err := output.write(event{"event": "started", "phase": "starting", "provider": o.provider, "model": o.model}); err != nil {
		fmt.Fprintln(os.Stderr, "write event:", err)
		return exitFailed
	}

	// Only this goroutine writes stdout. Keep reading after an output failure:
	// Agent emitters use blocking sends and must drain before App.Close.
	events := make(chan event, 64)
	go execute(ctx, o, events)
	var ticks <-chan time.Time
	if o.heartbeat > 0 {
		ticker := time.NewTicker(o.heartbeat)
		defer ticker.Stop()
		ticks = ticker.C
	}
	done := ctx.Done()
	var outputErr error
	write := func(e event) {
		if outputErr != nil {
			return
		}
		if outputErr = output.write(e); outputErr != nil {
			fmt.Fprintln(os.Stderr, "write event:", outputErr)
			cancel()
		}
	}
	code := exitFailed
	for {
		select {
		case e, ok := <-events:
			if !ok {
				if outputErr != nil {
					return exitFailed
				}
				return code
			}
			if finalCode, terminal := e["exit_code"].(int); terminal {
				code = finalCode
				// Do not emit a cancellation or heartbeat after the terminal event.
				done, ticks = nil, nil
			}
			write(e)
		case <-ticks:
			write(event{"event": "heartbeat"})
		case <-done:
			done = nil
			write(event{"event": "cancel_requested", "phase": "cancelling", "reason": ctx.Err().Error()})
		}
	}
}

// Reading stdin is explicit, so a forgotten prompt cannot silently hang startup.
// A blocked read must not prevent the overall context deadline from taking effect.
func readPrompt(ctx context.Context, o options) (string, error) {
	if o.promptFile == "" {
		return o.prompt, ctx.Err()
	}
	type result struct {
		data []byte
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		var r result
		if o.promptFile == "-" {
			r.data, r.err = io.ReadAll(os.Stdin)
		} else {
			r.data, r.err = os.ReadFile(o.promptFile)
		}
		ready <- r
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ready:
		if r.err != nil {
			return "", r.err
		}
		if !utf8.Valid(r.data) {
			return "", errors.New("task file or stdin must be UTF-8")
		}
		// Windows editors commonly write a UTF-8 BOM.
		text := strings.TrimPrefix(string(r.data), "\ufeff")
		if strings.TrimSpace(text) == "" {
			return "", errors.New("task input is empty")
		}
		return text, nil
	}
}
