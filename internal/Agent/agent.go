package Agent

import (
	"CodeS/internal/compact"
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/prompt"
	"CodeS/internal/session"
	"CodeS/internal/tools/manager"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

type Agent struct {
	clientMu      sync.Mutex
	client        llm.Client
	registry      manager.ToolCatalog
	maxTurns      int
	permissions   *manager.PermissionManager
	executor      *ToolExecutor
	BeforeRequest func(context.Context, *session.Session) error
}

type responseGenerator func(
	ctx context.Context,
	request *llm.Request,
	onEvent func(conversation.StreamEvent) error,
) (*conversation.Response, error)

func NewAgent(client llm.Client, registry manager.ToolCatalog, maxTurns int, permissions *manager.PermissionManager) *Agent {
	return &Agent{
		client:      client,
		registry:    registry,
		maxTurns:    maxTurns,
		permissions: permissions,
		executor:    NewToolExecutor(registry, permissions, defaultToolParallelism),
	}
}

func (a *Agent) Run(
	ctx context.Context,
	currentSession *session.Session,
	input string,
) <-chan Event {
	return a.run(
		ctx,
		currentSession,
		input,
		func(
			ctx context.Context,
			request *llm.Request,
			_ func(conversation.StreamEvent) error,
		) (*conversation.Response, error) {
			return a.client.Generate(ctx, request)
		},
	)
}

func (a *Agent) RunStream(
	ctx context.Context,
	currentSession *session.Session,
	input string,
) <-chan Event {
	return a.run(
		ctx,
		currentSession,
		input,
		func(
			ctx context.Context,
			request *llm.Request,
			onEvent func(conversation.StreamEvent) error,
		) (*conversation.Response, error) {
			return a.client.Stream(ctx, request, onEvent)
		},
	)
}

func (a *Agent) run(
	ctx context.Context,
	currentSession *session.Session,
	input string,
	generate responseGenerator,
) <-chan Event {
	eventQueue := make(chan Event, 50)
	go func() {
		defer close(eventQueue)
		emit := Emitter(func(event Event) {
			eventQueue <- event
		})

		if a != nil {
			if !a.clientMu.TryLock() {
				emit(RunFailed{Phase: "validation", Err: errors.New("agent is busy")})
				return
			}
			defer a.clientMu.Unlock()
		}

		if ctx == nil {
			emit(RunFailed{
				Step:  0,
				Phase: "validation",
				Err:   errors.New("context is nil"),
			})
			return
		}
		if currentSession == nil {
			emit(RunFailed{
				Step:  0,
				Phase: "validation",
				Err:   errors.New("session is nil"),
			})
			return
		}
		if a == nil || a.client == nil || a.registry == nil || a.executor == nil {
			emit(RunFailed{
				Step:  0,
				Phase: "validation",
				Err:   errors.New("agent dependencies are incomplete"),
			})
			return
		}
		if input == "" {
			emit(RunFailed{
				Step:  0,
				Phase: "validation",
				Err:   errors.New("input is empty"),
			})
			return
		}
		if a.maxTurns <= 0 {
			emit(RunFailed{
				Step:  0,
				Phase: "validation",
				Err:   errors.New("max turns must be greater than zero"),
			})
			return
		}
		if generate == nil {
			emit(RunFailed{
				Step:  0,
				Phase: "validation",
				Err:   errors.New("response generator is nil"),
			})
			return
		}

		emit(RunStarted{})

		if err := currentSession.AddUser(ctx, input); err != nil {
			emit(RunFailed{
				Step:  0,
				Phase: "persist_user",
				Err:   err,
			})
			return
		}

		totalUsage := conversation.Usage{}
		for turn := 1; turn <= a.maxTurns; turn++ {
			if a.BeforeRequest != nil {
				if err := a.BeforeRequest(ctx, currentSession); err != nil {
					emitOperationFailure(emit, turn, "task_notification", err)
					return
				}
			}
			emit(StepStarted{
				Step: turn,
			})
			resp, err := generate(ctx, new(llm.Request{
				Conversation: currentSession.LLMSnapshot(),
				Tools:        a.registry.Definitions(),
				Prompt:       prompt.BuildSystemPrompt(),
			}), func(event conversation.StreamEvent) error {
				if event.Type == conversation.StreamEventTextDelta {
					emit(TextDelta{
						Step:  turn,
						Delta: event.Delta,
					})
				}
				return nil
			})
			if err != nil {
				emitOperationFailure(emit, turn, "llm", err)
				return
			}
			if resp == nil {
				emit(RunFailed{
					Step:  turn,
					Phase: "llm_protocol",
					Err:   errors.New("LLM returned nil response"),
				})
				return
			}

			totalUsage.InputTokens += resp.Usage.InputTokens
			totalUsage.OutputTokens += resp.Usage.OutputTokens
			finishReason, err := classifyFinishReason(resp)
			if err != nil {
				emit(RunFailed{
					Step:  turn,
					Phase: "llm_protocol",
					Err:   err,
				})
				return
			}
			if finishReason == conversation.FinishReasonInsufficientSystemResource {
				emit(RunFailed{
					Step:  turn,
					Phase: "llm_provider",
					Err: fmt.Errorf(
						"LLM stopped because of %s",
						finishReason,
					),
				})
				return
			}

			if err := currentSession.AddAssistant(ctx, resp.Content, resp.ToolCalls); err != nil {
				emit(RunFailed{
					Step:  turn,
					Phase: "persist_assistant",
					Err:   err,
				})
				return
			}

			switch finishReason {
			case conversation.FinishReasonLength, conversation.FinishReasonContentFilter:
				emit(StepCompleted{Step: turn})
				emit(RunStopped{
					Step:   turn,
					Reason: string(finishReason),
				})
				return
			case conversation.FinishReasonStop:
				emit(StepCompleted{Step: turn})
				emit(RunCompleted{
					Result: RunResult{
						Steps:        turn,
						FinalContent: resp.Content,
						Usage:        totalUsage,
					},
				})
				return
			case conversation.FinishReasonToolCalls:
				// Continue below and execute the complete, validated calls.
			}

			emit(ActionProposed{
				Step:  turn,
				Calls: append([]conversation.ToolCall(nil), resp.ToolCalls...),
			})

			results, err := a.executor.Execute(
				ctx,
				turn,
				resp.ToolCalls,
				emit,
			)
			if err != nil {
				emitOperationFailure(emit, turn, "execute_tools", err)
				return
			}

			for _, record := range results {
				if err := currentSession.AddToolResult(ctx, record); err != nil {
					emit(RunFailed{
						Step:  turn,
						Phase: "persist_observation",
						Err:   err,
					})
					return
				}
				emit(ObservationRecorded{
					Step:   turn,
					Record: record,
				})
			}

			emit(StepCompleted{Step: turn})
		}

		emit(RunStopped{
			Step:   a.maxTurns,
			Reason: "max_turns",
		})
	}()
	return eventQueue

}

func classifyFinishReason(response *conversation.Response) (conversation.FinishReason, error) {
	finishReason := response.FinishReason
	if finishReason == "" {
		// Compatibility for providers that have not implemented FinishReason yet.
		if len(response.ToolCalls) > 0 {
			finishReason = conversation.FinishReasonToolCalls
		} else {
			finishReason = conversation.FinishReasonStop
		}
	}

	switch finishReason {
	case conversation.FinishReasonStop:
		if len(response.ToolCalls) != 0 {
			return "", fmt.Errorf(
				"finish reason is %q but response contains %d tool calls",
				finishReason,
				len(response.ToolCalls),
			)
		}
	case conversation.FinishReasonToolCalls:
		if len(response.ToolCalls) == 0 {
			return "", fmt.Errorf(
				"finish reason is %q but response contains no tool calls",
				finishReason,
			)
		}
	case conversation.FinishReasonLength, conversation.FinishReasonContentFilter:
		if len(response.ToolCalls) != 0 {
			return "", fmt.Errorf(
				"finish reason is %q but response contains incomplete tool calls",
				finishReason,
			)
		}
	case conversation.FinishReasonInsufficientSystemResource:
		// This is a valid provider outcome but an Agent run failure.
	default:
		return "", fmt.Errorf("unsupported LLM finish reason %q", finishReason)
	}

	return finishReason, nil
}

func emitOperationFailure(emit Emitter, step int, phase string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		emit(RunStopped{
			Step:   step,
			Reason: err.Error(),
		})
		return
	}
	emit(RunFailed{
		Step:  step,
		Phase: phase,
		Err:   err,
	})
}

func encodeToolError(toolName string, toolErr error) string {
	content, err := json.Marshal(map[string]any{
		"ok":      false,
		"tool":    toolName,
		"message": toolErr.Error(),
	})
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"message":"%s"}`, toolErr.Error())
	}
	return string(content)

}

func encodeToolDenied(toolName string) string {
	content, err := json.Marshal(map[string]any{
		"ok":      false,
		"tool":    toolName,
		"message": "does not have permission to access this resource",
	})
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"message":"%s"}`, toolName)
	}
	return string(content)
}

func (a *Agent) Compact(ctx context.Context, fmtMessage string) (string, error) {
	if a == nil {
		return "", errors.New("agent is nil")
	}
	if !a.clientMu.TryLock() {
		return "", errors.New("agent is busy")
	}
	defer a.clientMu.Unlock()
	if ctx == nil {
		return "", errors.New("context is nil")
	}
	if a == nil || a.client == nil {
		return "", errors.New("agent client is nil")
	}
	if fmtMessage == "" {
		return "", errors.New("conversation is empty")
	}
	req := new(llm.Request{
		Conversation: new(conversation.Conversation{
			Messages: []conversation.Message{
				{Role: conversation.RoleSystem, Content: compact.Prompt},
				{Role: conversation.RoleUser, Content: fmtMessage},
			},
		}),
	})
	resp, err := a.client.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("LLM returned nil compaction response")
	}
	if resp.Content == "" {
		return "", errors.New("LLM returned empty compaction summary")
	}
	return resp.Content, nil
}

func (a *Agent) SetClient(client llm.Client) error {
	if a == nil || client == nil {
		return errors.New("agent client is nil")
	}
	if !a.clientMu.TryLock() {
		return errors.New("agent is busy; wait for the current turn to finish")
	}
	defer a.clientMu.Unlock()
	a.client = client
	return nil
}
