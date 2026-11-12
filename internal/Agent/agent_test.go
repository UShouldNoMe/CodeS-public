package Agent

import (
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/persistent"
	"CodeS/internal/session"
	"CodeS/internal/tools"
	"CodeS/internal/tools/manager"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRunStreamEmitsEventsAcrossToolLoop(t *testing.T) {
	client := &scriptedStreamClient{
		responses: []*conversation.Response{
			{
				Usage: conversation.Usage{InputTokens: 10, OutputTokens: 2},
				ToolCalls: []conversation.ToolCall{
					{
						ID:        "call_echo",
						Name:      "Echo",
						Arguments: map[string]any{"text": "hello"},
					},
				},
			},
			{
				Content: "streamed answer",
				Usage:   conversation.Usage{InputTokens: 20, OutputTokens: 3},
			},
		},
	}
	registry := manager.NewRegistry()
	if err := registry.Register(echoTool{}); err != nil {
		t.Fatal(err)
	}
	database := &memoryStore{}
	currentSession, err := session.NewSession(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(client, registry, 4, manager.New(nil))

	events := collectEvents(agent.RunStream(
		context.Background(),
		currentSession,
		"question",
	))

	wantKinds := []string{
		"RunStarted",
		"StepStarted",
		"ActionProposed",
		"ActionStarted",
		"ActionFinished",
		"ObservationRecorded",
		"StepCompleted",
		"StepStarted",
		"TextDelta",
		"StepCompleted",
		"RunCompleted",
	}
	if got := eventKinds(events); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("event kinds = %#v, want %#v", got, wantKinds)
	}

	var streamedText strings.Builder
	var completed RunCompleted
	for _, event := range events {
		switch current := event.(type) {
		case TextDelta:
			streamedText.WriteString(current.Delta)
		case RunCompleted:
			completed = current
		}
	}
	if completed.Result.FinalContent != "streamed answer" {
		t.Fatalf("FinalContent = %q", completed.Result.FinalContent)
	}
	if streamedText.String() != completed.Result.FinalContent {
		t.Fatalf("streamed text = %q, want %q", streamedText.String(), completed.Result.FinalContent)
	}
	if completed.Result.Steps != 2 {
		t.Fatalf("Steps = %v, want 2", completed.Result.Steps)
	}
	if want := (conversation.Usage{InputTokens: 30, OutputTokens: 5}); completed.Result.Usage != want {
		t.Fatalf("Usage = %#v, want %#v", completed.Result.Usage, want)
	}
	if client.streamCalls != 2 {
		t.Fatalf("Stream calls = %v, want 2", client.streamCalls)
	}
	if client.generateCalls != 0 {
		t.Fatalf("Generate calls = %v, want 0", client.generateCalls)
	}

	wantRoles := []conversation.Role{
		conversation.RoleUser,
		conversation.RoleAssistant,
		conversation.RoleTool,
		conversation.RoleAssistant,
	}
	if len(database.messages) != len(wantRoles) {
		t.Fatalf("persisted messages = %v, want %v", len(database.messages), len(wantRoles))
	}
	for index, role := range wantRoles {
		if database.messages[index].Role != role {
			t.Fatalf(
				"message %v role = %q, want %q",
				index,
				database.messages[index].Role,
				role,
			)
		}
	}
}

func TestRunStreamRejectsEmptyInputBeforePersisting(t *testing.T) {
	database := &memoryStore{}
	currentSession, err := session.NewSession(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(
		&scriptedStreamClient{},
		manager.NewRegistry(),
		1,
		manager.New(nil),
	)

	events := collectEvents(agent.RunStream(
		context.Background(),
		currentSession,
		"",
	))
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one RunFailed", events)
	}
	failed, ok := events[0].(RunFailed)
	if !ok || failed.Phase != "validation" || failed.Err == nil {
		t.Fatalf("event = %#v, want validation RunFailed", events[0])
	}
	if len(database.messages) != 0 {
		t.Fatalf("persisted messages = %v, want 0", len(database.messages))
	}
}

func TestRunStreamDoesNotExecuteDeniedTool(t *testing.T) {
	client := &scriptedStreamClient{
		responses: []*conversation.Response{
			{
				ToolCalls: []conversation.ToolCall{
					{
						ID:        "call_write",
						Name:      "Write",
						Arguments: map[string]any{"content": "denied"},
					},
				},
			},
			{Content: "permission denied"},
		},
	}
	executed := false
	registry := manager.NewRegistry()
	if err := registry.Register(trackingTool{executed: &executed}); err != nil {
		t.Fatal(err)
	}
	database := &memoryStore{}
	currentSession, err := session.NewSession(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	permissions := manager.New(func(context.Context, manager.Request) (bool, error) {
		return false, nil
	})
	agent := NewAgent(client, registry, 4, permissions)

	events := collectEvents(agent.RunStream(
		context.Background(),
		currentSession,
		"write something",
	))
	if executed {
		t.Fatal("denied tool was executed")
	}

	var completed RunCompleted
	var observation ObservationRecorded
	for _, event := range events {
		switch current := event.(type) {
		case ActionStarted, ActionFinished:
			t.Fatalf("denied tool emitted execution event %#v", event)
		case ObservationRecorded:
			observation = current
		case RunCompleted:
			completed = current
		}
	}
	if completed.Result.FinalContent != "permission denied" {
		t.Fatalf("FinalContent = %q, want permission denied", completed.Result.FinalContent)
	}
	if observation.Record.Status != conversation.ToolResultDenied {
		t.Fatalf("observation status = %q, want denied", observation.Record.Status)
	}
	if len(database.messages) != 4 {
		t.Fatalf("persisted messages = %v, want 4", len(database.messages))
	}
	toolResult := database.messages[2].ToolResult
	if toolResult == nil || !strings.Contains(toolResult.Content, `"ok":false`) {
		t.Fatalf("denied ToolResult = %#v", toolResult)
	}
}

func TestRunStreamReturnsToolResultPersistenceError(t *testing.T) {
	wantPersistErr := errors.New("persist tool result")
	registry := manager.NewRegistry()
	if err := registry.Register(failingTool{}); err != nil {
		t.Fatal(err)
	}
	database := &memoryStore{
		appendErrAt: 3,
		appendErr:   wantPersistErr,
	}
	currentSession, err := session.NewSession(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedStreamClient{
		responses: []*conversation.Response{
			{
				ToolCalls: []conversation.ToolCall{
					{ID: "call_fail", Name: "Fail"},
				},
			},
		},
	}
	agent := NewAgent(client, registry, 2, manager.New(nil))

	events := collectEvents(agent.RunStream(
		context.Background(),
		currentSession,
		"run failing tool",
	))

	var failed RunFailed
	var sawFinished bool
	for _, event := range events {
		switch current := event.(type) {
		case ActionFinished:
			sawFinished = true
			if current.Record.Status != conversation.ToolResultExecutionError {
				t.Fatalf("finished status = %q, want execution_error", current.Record.Status)
			}
		case ObservationRecorded:
			t.Fatal("observation emitted before persistence succeeded")
		case RunFailed:
			failed = current
		}
	}
	if !sawFinished {
		t.Fatal("missing ActionFinished")
	}
	if failed.Phase != "persist_observation" || !errors.Is(failed.Err, wantPersistErr) {
		t.Fatalf("RunFailed = %#v, want persistence error", failed)
	}
}

func TestRunStreamMapsFinishReasonToTerminalEvent(t *testing.T) {
	tests := []struct {
		name               string
		finishReason       conversation.FinishReason
		wantStoppedReason  string
		wantFailedPhase    string
		wantPersistedCount int
	}{
		{
			name:               "length",
			finishReason:       conversation.FinishReasonLength,
			wantStoppedReason:  string(conversation.FinishReasonLength),
			wantPersistedCount: 2,
		},
		{
			name:               "content filter",
			finishReason:       conversation.FinishReasonContentFilter,
			wantStoppedReason:  string(conversation.FinishReasonContentFilter),
			wantPersistedCount: 2,
		},
		{
			name:               "insufficient system resource",
			finishReason:       conversation.FinishReasonInsufficientSystemResource,
			wantFailedPhase:    "llm_provider",
			wantPersistedCount: 1,
		},
		{
			name:               "unknown provider reason",
			finishReason:       conversation.FinishReason("future_reason"),
			wantFailedPhase:    "llm_protocol",
			wantPersistedCount: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := &memoryStore{}
			currentSession, err := session.NewSession(context.Background(), database)
			if err != nil {
				t.Fatal(err)
			}
			agent := NewAgent(
				&scriptedStreamClient{responses: []*conversation.Response{{
					Content:      "partial",
					FinishReason: test.finishReason,
				}}},
				manager.NewRegistry(),
				2,
				manager.New(nil),
			)

			events := collectEvents(agent.RunStream(
				context.Background(),
				currentSession,
				"question",
			))
			if len(events) == 0 {
				t.Fatal("no events emitted")
			}

			switch terminal := events[len(events)-1].(type) {
			case RunStopped:
				if terminal.Reason != test.wantStoppedReason || test.wantFailedPhase != "" {
					t.Fatalf("terminal = %#v", terminal)
				}
			case RunFailed:
				if terminal.Phase != test.wantFailedPhase || test.wantStoppedReason != "" {
					t.Fatalf("terminal = %#v", terminal)
				}
			default:
				t.Fatalf("terminal = %#v, want RunStopped or RunFailed", terminal)
			}

			if len(database.messages) != test.wantPersistedCount {
				t.Fatalf(
					"persisted messages = %v, want %v",
					len(database.messages),
					test.wantPersistedCount,
				)
			}
		})
	}
}

func TestRunStreamRejectsInconsistentFinishReason(t *testing.T) {
	database := &memoryStore{}
	currentSession, err := session.NewSession(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(
		&scriptedStreamClient{responses: []*conversation.Response{{
			FinishReason: conversation.FinishReasonToolCalls,
		}}},
		manager.NewRegistry(),
		2,
		manager.New(nil),
	)

	events := collectEvents(agent.RunStream(
		context.Background(),
		currentSession,
		"question",
	))
	failed, ok := events[len(events)-1].(RunFailed)
	if !ok || failed.Phase != "llm_protocol" {
		t.Fatalf("terminal = %#v, want llm_protocol RunFailed", events[len(events)-1])
	}
	if len(database.messages) != 1 {
		t.Fatalf("persisted messages = %v, want only user message", len(database.messages))
	}
}

func collectEvents(queue <-chan Event) []Event {
	var events []Event
	for event := range queue {
		events = append(events, event)
	}
	return events
}

func eventKinds(events []Event) []string {
	kinds := make([]string, 0, len(events))
	for _, event := range events {
		switch event.(type) {
		case RunStarted:
			kinds = append(kinds, "RunStarted")
		case StepStarted:
			kinds = append(kinds, "StepStarted")
		case TextDelta:
			kinds = append(kinds, "TextDelta")
		case ActionProposed:
			kinds = append(kinds, "ActionProposed")
		case ActionStarted:
			kinds = append(kinds, "ActionStarted")
		case ActionFinished:
			kinds = append(kinds, "ActionFinished")
		case ObservationRecorded:
			kinds = append(kinds, "ObservationRecorded")
		case StepCompleted:
			kinds = append(kinds, "StepCompleted")
		case RunCompleted:
			kinds = append(kinds, "RunCompleted")
		case RunStopped:
			kinds = append(kinds, "RunStopped")
		case RunFailed:
			kinds = append(kinds, "RunFailed")
		}
	}
	return kinds
}

type scriptedStreamClient struct {
	responses     []*conversation.Response
	streamCalls   int
	generateCalls int
}

func (c *scriptedStreamClient) Generate(
	context.Context,
	*llm.Request,
) (*conversation.Response, error) {
	c.generateCalls++
	return nil, errors.New("Generate should not be called")
}

func (c *scriptedStreamClient) Stream(
	_ context.Context,
	_ *llm.Request,
	onEvent func(conversation.StreamEvent) error,
) (*conversation.Response, error) {
	if c.streamCalls >= len(c.responses) {
		return nil, errors.New("no scripted response")
	}
	response := c.responses[c.streamCalls]
	c.streamCalls++
	if response.Content != "" {
		if err := onEvent(conversation.StreamEvent{
			Type:  conversation.StreamEventTextDelta,
			Delta: response.Content,
		}); err != nil {
			return nil, err
		}
	}
	if err := onEvent(conversation.StreamEvent{
		Type:         conversation.StreamEventEnd,
		FinishReason: response.FinishReason,
		Usage:        response.Usage,
	}); err != nil {
		return nil, err
	}
	return response, nil
}

type echoTool struct{}

func (echoTool) Definition() tools.ToolDef {
	return tools.ToolDef{
		Name:       "Echo",
		Type:       "object",
		Properties: map[string]any{},
		Authority:  tools.CategoryRead,
	}
}

func (echoTool) Execute(_ context.Context, arguments map[string]any) ([]any, error) {
	return []any{arguments["text"]}, nil
}

type trackingTool struct {
	executed *bool
}

func (trackingTool) Definition() tools.ToolDef {
	return tools.ToolDef{
		Name:      "Write",
		Type:      "object",
		Authority: tools.CategoryWrite,
	}
}

func (t trackingTool) Execute(context.Context, map[string]any) ([]any, error) {
	*t.executed = true
	return nil, nil
}

type failingTool struct{}

func (failingTool) Definition() tools.ToolDef {
	return tools.ToolDef{
		Name:      "Fail",
		Type:      "object",
		Authority: tools.CategoryRead,
	}
}

func (failingTool) Execute(context.Context, map[string]any) ([]any, error) {
	return nil, errors.New("tool execution failed")
}

type memoryStore struct {
	messages    []conversation.Message
	compaction  *persistent.Compaction
	appendCalls int
	appendErrAt int
	appendErr   error
}

func (s *memoryStore) AppendCompaction(_ context.Context, _ string, compaction persistent.Compaction) error {
	copy := compaction
	s.compaction = &copy
	return nil
}

func (s *memoryStore) AppendMessage(
	_ context.Context,
	_ string,
	message conversation.Message,
) error {
	s.appendCalls++
	if s.appendCalls == s.appendErrAt {
		return s.appendErr
	}
	s.messages = append(s.messages, message)
	return nil
}

func (*memoryStore) CreateSession(context.Context) (string, error) {
	return "1", nil
}

func (*memoryStore) LoadMessages(context.Context, string) ([]conversation.Message, error) {
	return nil, nil
}

func (s *memoryStore) LoadCompaction(context.Context, string) (*persistent.Compaction, error) {
	if s.compaction == nil {
		return nil, nil
	}
	copy := *s.compaction
	return &copy, nil
}

func (*memoryStore) ListSessions(context.Context) ([]persistent.SessionInfo, error) {
	return []persistent.SessionInfo{{FileName: "1"}}, nil
}

func (*memoryStore) DeleteSession(context.Context, string) error {
	return nil
}

func (*memoryStore) TouchSession(context.Context, string) error {
	return nil
}
