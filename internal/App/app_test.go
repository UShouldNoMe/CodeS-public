package App

import (
	"CodeS/internal/Agent"
	"CodeS/internal/MCP"
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/persistent"
	toolmanager "CodeS/internal/tools/manager"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestAppChatAndSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	database := newMemoryStore()
	registry := toolmanager.NewRegistry()
	agent := Agent.NewAgent(
		staticClient{content: "hello"},
		registry,
		2,
		toolmanager.New(nil),
	)
	manager, err := MCP.NewManager(nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(agent, database, manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	if currentFileName := application.CurrentSessionFileName(); currentFileName != "" {
		t.Fatalf("CurrentSessionFileName() = %v, want empty", currentFileName)
	}
	withoutSession := collectAgentEvents(application.ChatStream(ctx, "question"))
	if len(withoutSession) != 1 {
		t.Fatalf("ChatStream() events = %#v, want one RunFailed", withoutSession)
	}
	if _, ok := withoutSession[0].(Agent.RunFailed); !ok {
		t.Fatalf("ChatStream() event = %#v, want RunFailed", withoutSession[0])
	}

	firstFileName, err := application.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if firstFileName != "1" {
		t.Fatalf("NewSession() file name = %v, want 1", firstFileName)
	}

	events := collectAgentEvents(application.ChatStream(ctx, "question"))
	var completed Agent.RunCompleted
	for _, event := range events {
		if current, ok := event.(Agent.RunCompleted); ok {
			completed = current
		}
	}
	if completed.Result.FinalContent != "hello" {
		t.Fatalf("ChatStream() content = %q, want hello", completed.Result.FinalContent)
	}
	currentMessages := application.CurrentMessages()
	if len(currentMessages) != 2 {
		t.Fatalf("CurrentMessages() length = %v, want 2", len(currentMessages))
	}
	if currentMessages[0].Role != conversation.RoleUser || currentMessages[0].Content != "question" {
		t.Fatalf("CurrentMessages()[0] = %#v, want persisted user question", currentMessages[0])
	}
	if currentMessages[1].Role != conversation.RoleAssistant || currentMessages[1].Content != "hello" {
		t.Fatalf("CurrentMessages()[1] = %#v, want persisted assistant answer", currentMessages[1])
	}

	if currentFileName := application.CurrentSessionFileName(); currentFileName != firstFileName {
		t.Fatalf("CurrentSessionFileName() = %v, want %v", currentFileName, firstFileName)
	}
	secondFileName, err := application.NewSession(ctx)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if secondFileName != "2" {
		t.Fatalf("NewSession() file name = %v, want 2", secondFileName)
	}

	sessions, err := application.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if want := []persistent.SessionInfo{{FileName: "1"}, {FileName: "2"}}; !reflect.DeepEqual(sessions, want) {
		t.Fatalf("ListSessions() = %#v, want %#v", sessions, want)
	}

	if err := application.SwitchSession(ctx, firstFileName); err != nil {
		t.Fatalf("SwitchSession() error = %v", err)
	}
	if err := application.SwitchSession(ctx, "999"); !errors.Is(err, persistent.ErrSessionNotFound) {
		t.Fatalf("SwitchSession() error = %v, want ErrSessionNotFound", err)
	}
	if currentFileName := application.CurrentSessionFileName(); currentFileName != firstFileName {
		t.Fatalf("failed switch changed current session to %v", currentFileName)
	}

	if err := application.DeleteSession(ctx, firstFileName); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if currentFileName := application.CurrentSessionFileName(); currentFileName != "" {
		t.Fatalf("CurrentSessionFileName() = %v after deleting current session, want empty", currentFileName)
	}
	withoutSession = collectAgentEvents(application.ChatStream(ctx, "question"))
	if len(withoutSession) != 1 {
		t.Fatalf("ChatStream() events = %#v, want one RunFailed", withoutSession)
	}
	if _, ok := withoutSession[0].(Agent.RunFailed); !ok {
		t.Fatalf("ChatStream() event = %#v, want RunFailed", withoutSession[0])
	}

	application.Close()
	application.Close()
	if database.closeCalls != 1 {
		t.Fatalf("Close() calls = %v, want 1", database.closeCalls)
	}
	if err := manager.ConnectAll(ctx); !errors.Is(err, MCP.ErrManagerClosed) {
		t.Fatalf("ConnectAll() after App.Close() error = %v, want ErrManagerClosed", err)
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	database := newMemoryStore()
	registry := toolmanager.NewRegistry()
	manager, err := MCP.NewManager(nil)
	if err != nil {
		t.Fatal(err)
	}
	agent := Agent.NewAgent(staticClient{}, registry, 1, toolmanager.New(nil))

	tests := []struct {
		name     string
		agent    *Agent.Agent
		store    persistent.Store
		manager  *MCP.Manager
		registry *toolmanager.Manager
	}{
		{name: "agent", store: database, manager: manager, registry: registry},
		{name: "persistent", agent: agent, manager: manager, registry: registry},
		{name: "manager", agent: agent, store: database, registry: registry},
		{name: "registry", agent: agent, store: database, manager: manager},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(
				test.agent,
				test.store,
				test.manager,
				test.registry,
			); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestCompactContextUsesSummaryForLLMAndKeepsFullHistory(t *testing.T) {
	ctx := context.Background()
	database := newMemoryStore()
	registry := toolmanager.NewRegistry()
	client := &compactRecordingClient{summary: "important earlier context"}
	manager, err := MCP.NewManager(nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(
		Agent.NewAgent(client, registry, 2, toolmanager.New(nil)),
		database,
		manager,
		registry,
	)
	if err != nil {
		t.Fatal(err)
	}
	firstFileName, err := application.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(application.ChatStream(ctx, "old question"))
	before := application.CurrentMessages()
	if len(before) != 2 {
		t.Fatalf("history before compact = %#v", before)
	}

	if err := application.CompactContext(ctx); err != nil {
		t.Fatal(err)
	}
	if got := application.CurrentMessages(); !reflect.DeepEqual(got, before) {
		t.Fatalf("compact changed visible history: %#v", got)
	}
	if client.generateRequest == nil || len(client.generateRequest.Conversation.Messages) != 2 {
		t.Fatalf("summary request did not receive existing context: %#v", client.generateRequest)
	}

	collectAgentEvents(application.ChatStream(ctx, "new question"))
	request := client.streamRequests[len(client.streamRequests)-1]
	if got := request.Conversation.Messages; len(got) != 2 ||
		got[0].Role != conversation.RoleUser || !strings.Contains(got[0].Content, client.summary) ||
		got[1].Content != "new question" {
		t.Fatalf("post-compact LLM context = %#v, want summary plus new question", got)
	}
	if got := application.CurrentMessages(); len(got) != 4 || !reflect.DeepEqual(got[:2], before) {
		t.Fatalf("visible history was not preserved after continuing: %#v", got)
	}

	if _, err := application.NewSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := application.SwitchSession(ctx, firstFileName); err != nil {
		t.Fatal(err)
	}
	if got := application.CurrentMessages(); len(got) != 4 || !reflect.DeepEqual(got[:2], before) {
		t.Fatalf("resumed visible history = %#v", got)
	}
	resumedContext := application.currentSession.LLMSnapshot().Messages
	if len(resumedContext) != 3 || !strings.Contains(resumedContext[0].Content, client.summary) {
		t.Fatalf("resumed LLM context = %#v", resumedContext)
	}
}

func collectAgentEvents(queue <-chan Agent.Event) []Agent.Event {
	var events []Agent.Event
	for event := range queue {
		events = append(events, event)
	}
	return events
}

type staticClient struct {
	content string
}

type compactRecordingClient struct {
	summary         string
	generateRequest *llm.Request
	streamRequests  []*llm.Request
}

func (c *compactRecordingClient) Generate(_ context.Context, request *llm.Request) (*conversation.Response, error) {
	c.generateRequest = request
	return &conversation.Response{Content: c.summary}, nil
}

func (c *compactRecordingClient) Stream(
	_ context.Context,
	request *llm.Request,
	onEvent func(conversation.StreamEvent) error,
) (*conversation.Response, error) {
	c.streamRequests = append(c.streamRequests, request)
	content := "answer"
	if err := onEvent(conversation.StreamEvent{Type: conversation.StreamEventTextDelta, Delta: content}); err != nil {
		return nil, err
	}
	return &conversation.Response{Content: content}, nil
}

func (c staticClient) Generate(context.Context, *llm.Request) (*conversation.Response, error) {
	return &conversation.Response{Content: c.content}, nil
}

func (c staticClient) Stream(_ context.Context, _ *llm.Request, onEvent func(conversation.StreamEvent) error) (*conversation.Response, error) {
	if c.content != "" {
		if err := onEvent(conversation.StreamEvent{Type: conversation.StreamEventTextDelta, Delta: c.content}); err != nil {
			return nil, err
		}
	}
	if err := onEvent(conversation.StreamEvent{Type: conversation.StreamEventEnd}); err != nil {
		return nil, err
	}
	return &conversation.Response{Content: c.content}, nil
}

type memoryStore struct {
	nextID     int
	messages   map[string][]conversation.Message
	compaction map[string]*persistent.Compaction
	closeCalls int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		messages:   make(map[string][]conversation.Message),
		compaction: make(map[string]*persistent.Compaction),
	}
}

func (s *memoryStore) CreateSession(context.Context) (string, error) {
	s.nextID++
	id := fmt.Sprint(s.nextID)
	s.messages[id] = nil
	return id, nil
}

func (s *memoryStore) AppendMessage(_ context.Context, id string, message conversation.Message) error {
	if _, ok := s.messages[id]; !ok {
		return persistent.ErrSessionNotFound
	}
	s.messages[id] = append(s.messages[id], message)
	return nil
}

func (s *memoryStore) AppendCompaction(_ context.Context, id string, compaction persistent.Compaction) error {
	if _, ok := s.messages[id]; !ok {
		return persistent.ErrSessionNotFound
	}
	copy := compaction
	s.compaction[id] = &copy
	return nil
}

func (s *memoryStore) LoadMessages(_ context.Context, id string) ([]conversation.Message, error) {
	messages, ok := s.messages[id]
	if !ok {
		return nil, persistent.ErrSessionNotFound
	}
	return append([]conversation.Message(nil), messages...), nil
}

func (s *memoryStore) LoadCompaction(_ context.Context, id string) (*persistent.Compaction, error) {
	if _, ok := s.messages[id]; !ok {
		return nil, persistent.ErrSessionNotFound
	}
	if s.compaction[id] == nil {
		return nil, nil
	}
	copy := *s.compaction[id]
	return &copy, nil
}

func (s *memoryStore) ListSessions(context.Context) ([]persistent.SessionInfo, error) {
	ret := make([]persistent.SessionInfo, 0, len(s.messages))
	for id := range s.messages {
		ret = append(ret, persistent.SessionInfo{FileName: id})
	}
	sort.Slice(ret, func(i, j int) bool { return ret[i].FileName < ret[j].FileName })
	return ret, nil
}

func (s *memoryStore) DeleteSession(_ context.Context, id string) error {
	if _, ok := s.messages[id]; !ok {
		return persistent.ErrSessionNotFound
	}
	delete(s.messages, id)
	delete(s.compaction, id)
	return nil
}

func (*memoryStore) TouchSession(context.Context, string) error { return nil }

func (s *memoryStore) Close() {
	s.closeCalls++
}
