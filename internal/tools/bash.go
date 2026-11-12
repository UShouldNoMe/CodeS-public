package tools

import (
	sandboxpkg "CodeS/internal/sandbox"
	workspace2 "CodeS/internal/workspace"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

const (
	defaultCommandTimeoutSeconds = 120
	maxCommandTimeoutSeconds     = 600
)

type Bash struct {
	workspace      workspace2.Workspace
	sandbox        sandboxpkg.Sandbox
	sandboxConfig  sandboxpkg.Config
	requireSandbox bool
}

type BashResult struct {
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	ExitCode      int    `json:"exit_code"`
	TimedOut      bool   `json:"timed_out"`
	Truncated     bool   `json:"truncated"`
	CommandStatus string `json:"command_status"`
}

func NewBash(workspace workspace2.Workspace) Bash {
	return Bash{workspace: workspace}
}

// WithSandbox configures a sandbox. Wrapping errors always fail closed.
// RequireSandbox also rejects unavailable backends for isolated workspaces.
func (b Bash) WithSandbox(
	commandSandbox sandboxpkg.Sandbox,
	config sandboxpkg.Config,
) Bash {
	b.sandbox = commandSandbox
	b.sandboxConfig = config
	return b
}

// RequireSandbox prevents execution when no OS sandbox is available.
func (b Bash) RequireSandbox() Bash {
	b.requireSandbox = true
	return b
}

func (b Bash) Definition() ToolDef {
	return ToolDef{
		Name:        "Bash",
		Description: "在工作区根目录执行 shell 命令，返回 stdout、stderr 和退出码。",
		Type:        "object",
		Properties: map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "需要执行的 shell 命令。",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "超时秒数，默认 120，最大 600。",
				"minimum":     1,
				"maximum":     maxCommandTimeoutSeconds,
			},
		},
		Required:  []string{"command"},
		Authority: CategoryCommand,
	}
}

func (b Bash) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	if err := workspace2.ContextError(ctx); err != nil {
		return nil, err
	}
	commandText, err := workspace2.RequiredStringArgument(arguments, "command", false)
	if err != nil {
		return nil, err
	}
	timeoutSeconds, err := workspace2.OptionalIntArgument(
		arguments,
		"timeout_seconds",
		defaultCommandTimeoutSeconds,
		1,
		maxCommandTimeoutSeconds,
	)
	if err != nil {
		return nil, err
	}

	commandContext, cancel := context.WithTimeout(
		ctx,
		time.Duration(timeoutSeconds)*time.Second,
	)
	defer cancel()

	commandSpec := regularShellCommand(commandText)
	if b.sandbox != nil && b.sandbox.Available() {
		wrappedCommand, wrapErr := b.sandbox.Wrap(commandText, b.sandboxConfig)
		if wrapErr != nil {
			return nil, fmt.Errorf("sandbox wrapping failed: %w", wrapErr)
		}
		commandSpec = wrappedCommand
	} else if b.requireSandbox {
		return nil, fmt.Errorf("isolated Bash requires an OS sandbox: %w", sandboxpkg.ErrUnavailable)
	}

	command := exec.CommandContext(
		commandContext,
		commandSpec.Executable,
		commandSpec.Args...,
	)
	command.Dir = b.workspace.Root()
	command.WaitDelay = 2 * time.Second
	if b.requireSandbox {
		stopChildren := isolateCommandLifetime(command)
		defer stopChildren()
	}

	var stdout limitedBuffer
	var stderr limitedBuffer
	stdout.limit = workspace2.MaxToolOutputChars
	stderr.limit = workspace2.MaxToolOutputChars
	command.Stdout = &stdout
	command.Stderr = &stderr

	runErr := command.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	result := BashResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ExitCode:  0,
		TimedOut:  errors.Is(commandContext.Err(), context.DeadlineExceeded),
		Truncated: stdout.truncated || stderr.truncated,
	}
	if runErr != nil {
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			result.ExitCode = exitError.ExitCode()
		} else if result.TimedOut {
			result.ExitCode = -1
		} else {
			return nil, runErr
		}
	}
	switch {
	case result.TimedOut:
		result.CommandStatus = "timed_out"
	case result.ExitCode != 0:
		result.CommandStatus = "failed"
	default:
		result.CommandStatus = "success"
	}
	return []any{result}, nil
}

// regularShellCommand describes the non-sandboxed fallback command for the
// current platform. A sandbox wrapper, when available, replaces this entire
// executable-and-arguments pair rather than being parsed by another shell.
func regularShellCommand(commandText string) sandboxpkg.Command {
	if runtime.GOOS == "windows" {
		return sandboxpkg.Command{
			Executable: "cmd.exe",
			Args:       []string{"/D", "/S", "/C", commandText},
		}
	}

	return sandboxpkg.Command{
		Executable: "/bin/sh",
		Args:       []string{"-c", commandText},
	}
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return originalLength, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return originalLength, nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}
