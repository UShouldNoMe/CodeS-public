package main

import (
	"CodeS/internal/Agent"
	"CodeS/internal/App"
	"CodeS/internal/conversation"
	"CodeS/internal/tools/manager"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func failure(phase string, err error) event {
	return event{"event": "failed", "phase": phase, "error": err.Error(), "exit_code": exitFailed}
}

func addCommandResultFields(e event, command *conversation.CommandResult) event {
	if command != nil {
		e["command_status"] = command.Status
		e["command_exit_code"] = command.ExitCode
		e["command_timed_out"] = command.TimedOut
	}
	return e
}

// execute owns the App and drains the Agent channel before closing it. The
// monitor stays responsive (including during startup and background cleanup).
func execute(ctx context.Context, o options, out chan<- event) {
	var application *App.App
	var sessionFile string
	result := failure("agent", errors.New("Agent event stream ended without a terminal event"))
	defer func() {
		if application != nil {
			out <- event{"event": "closing", "phase": "closing"}
			if err := application.Close(); err != nil {
				result["cleanup_error"] = err.Error()
				if result["exit_code"] == exitOK {
					result["event"], result["phase"] = "failed", "closing"
					result["error"], result["exit_code"] = err.Error(), exitFailed
				}
			}
		}
		if err := ctx.Err(); err != nil {
			result["event"], result["phase"], result["reason"] = "stopped", "finished", err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				result["exit_code"] = exitTimeout
			} else {
				result["exit_code"] = exitCanceled
			}
		}
		if sessionFile != "" {
			result["session_file"] = sessionFile
		}
		if message, ok := result["error"]; ok {
			fmt.Fprintln(os.Stderr, "codes-run:", message)
		}
		out <- result
		close(out)
	}()

	out <- event{"event": "status", "phase": "reading_prompt"}
	prompt, err := readPrompt(ctx, o)
	if err != nil {
		result = failure("input", err)
		return
	}
	root, err := filepath.Abs(o.workspace)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		result = failure("workspace", err)
		return
	}
	out <- event{"event": "status", "phase": "initializing", "workspace": root}
	application, err = App.Build(ctx, App.BuildOptions{
		WorkspaceRoot: root, ConfigPath: o.config, MaxTurns: o.maxTurns,
	})
	if err != nil {
		result = failure("initialization", err)
		return
	}
	if o.executionMode != "" {
		if err := application.SetExecutionMode(manager.ExecutionMode(o.executionMode)); err != nil {
			result = failure("permissions", err)
			return
		}
	}
	if err := application.SwitchModel(ctx, o.provider, o.model); err != nil {
		result = failure("model", err)
		return
	}
	fileName, err := application.NewSession(ctx)
	if err != nil {
		result = failure("session", err)
		return
	}
	sessionFile = filepath.Join(root, ".codes", "sessions", fileName)
	out <- event{
		"event": "session_created", "phase": "ready", "session_file": sessionFile,
		"execution_mode": application.ExecutionMode(),
	}
	for message := range application.ChatStream(ctx, prompt) {
		switch e := message.(type) {
		case Agent.RunStarted:
			out <- event{"event": "run_started", "phase": "persisting"}
		case Agent.StepStarted:
			out <- event{"event": "step_started", "phase": "waiting_model", "step": e.Step}
		case Agent.TextDelta:
			out <- event{"event": "text_delta", "phase": "waiting_model", "step": e.Step, "delta": e.Delta}
		case Agent.ActionProposed:
			out <- event{"event": "tools_proposed", "phase": "executing_tools", "step": e.Step, "count": len(e.Calls)}
		case Agent.ActionStarted:
			out <- event{"event": "tool_started", "phase": "executing_tools", "step": e.Step, "tool": e.Call.Name, "call_id": e.Call.ID}
		case Agent.ActionFinished:
			out <- addCommandResultFields(event{"event": "tool_finished", "step": e.Step, "tool": e.Call.Name, "call_id": e.Call.ID, "status": e.Record.Status}, e.Record.Command)
		case Agent.ObservationRecorded:
			// Denied/unknown tools may have no ActionStarted/ActionFinished pair.
			out <- addCommandResultFields(event{"event": "observation_recorded", "phase": "persisting", "step": e.Step, "tool": e.Record.ToolName, "call_id": e.Record.ToolCallID, "status": e.Record.Status}, e.Record.Command)
		case Agent.StepCompleted:
			out <- event{"event": "step_completed", "phase": "between_steps", "step": e.Step}
		case Agent.RunCompleted:
			result = event{
				"event": "completed", "phase": "finished", "exit_code": exitOK,
				"step": e.Result.Steps, "steps": e.Result.Steps, "final_content": e.Result.FinalContent,
				"usage": event{"input_tokens": e.Result.Usage.InputTokens, "output_tokens": e.Result.Usage.OutputTokens},
			}
			out <- event{"event": "loop_completed", "phase": "closing", "step": e.Result.Steps}
		case Agent.RunStopped:
			result = event{"event": "stopped", "phase": "finished", "step": e.Step, "reason": e.Reason, "exit_code": exitStopped}
		case Agent.RunFailed:
			result = failure(e.Phase, e.Err)
			result["step"] = e.Step
		}
	}
}
