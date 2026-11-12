package subAgent

import (
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/session"
	"CodeS/internal/tools/manager"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubClient struct {
	stream func(context.Context, *llm.Request) (*conversation.Response, error)
}

func (c stubClient) Generate(context.Context, *llm.Request) (*conversation.Response, error) {
	return nil, errors.New("unexpected Generate call")
}

func (c stubClient) Stream(ctx context.Context, request *llm.Request, _ func(conversation.StreamEvent) error) (*conversation.Response, error) {
	return c.stream(ctx, request)
}

func newSubAgentTool(client llm.Client, tasks *sync.Map) *Tool {
	return &Tool{
		Specs: BuiltinSpecs,
		Environment: Environment{
			ClientProvider: func() llm.Client { return client },
			Catalog:        manager.NewRegistry(),
			Permissions:    manager.New(nil),
			Tasks:          tasks,
		},
	}
}

func resultMap(t *testing.T, output []any) map[string]any {
	t.Helper()
	if len(output) != 1 {
		t.Fatalf("output length = %d, want 1", len(output))
	}
	result, ok := output[0].(map[string]any)
	if !ok {
		t.Fatalf("output[0] = %T, want map[string]any", output[0])
	}
	return result
}

func TestForegroundSubAgentReturnsResult(t *testing.T) {
	for _, role := range []string{"explorer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			var tasks sync.Map
			client := stubClient{stream: func(_ context.Context, request *llm.Request) (*conversation.Response, error) {
				messages := request.Conversation.Messages
				if len(messages) != 2 || messages[0].Role != conversation.RoleSystem || messages[1].Content != "inspect this" {
					t.Errorf("child conversation = %#v, want role prompt and assigned task", messages)
				}
				return &conversation.Response{Content: role + " result"}, nil
			}}
			tool := newSubAgentTool(client, &tasks)
			output, err := tool.Execute(context.Background(), map[string]any{
				"prompt": "inspect this", "subagent_type": role,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			result := resultMap(t, output)
			if result["status"] != "completed" || result["result"] != role+" result" {
				t.Fatalf("Execute() result = %#v", result)
			}
			if _, exists := result["task_id"]; exists {
				t.Fatalf("foreground result unexpectedly contains task_id: %#v", result)
			}
			tasks.Range(func(key, value any) bool {
				t.Errorf("foreground run stored task %v: %#v", key, value)
				return false
			})
		})
	}
}

func TestForegroundSubAgentCancellation(t *testing.T) {
	var tasks sync.Map
	started := make(chan struct{})
	client := stubClient{stream: func(ctx context.Context, _ *llm.Request) (*conversation.Response, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	tool := newSubAgentTool(client, &tasks)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(ctx, map[string]any{"prompt": "inspect", "subagent_type": "explorer"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("foreground child did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute() error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("foreground Execute did not return after cancellation")
	}
	tasks.Range(func(key, value any) bool {
		t.Errorf("cancelled foreground run stored task %v: %#v", key, value)
		return false
	})
}

func TestForkReturnsTaskIDBeforeCompletion(t *testing.T) {
	var tasks sync.Map
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	finished := make(chan Task, 1)
	client := stubClient{stream: func(_ context.Context, request *llm.Request) (*conversation.Response, error) {
		if !strings.Contains(request.Conversation.Messages[len(request.Conversation.Messages)-1].Content, "fork task") {
			t.Error("fork task missing from child conversation")
		}
		close(started)
		<-release
		return &conversation.Response{Content: "fork result"}, nil
	}}
	tool := newSubAgentTool(client, &tasks)
	tool.Environment.ParentSessionProvider = func() *session.Session {
		return session.NewTransient(conversation.New())
	}
	tool.Environment.TaskFinished = func(task Task) { finished <- task }
	output, err := tool.Execute(context.Background(), map[string]any{"prompt": "fork task", "subagent_type": "fork"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	result := resultMap(t, output)
	taskID, ok := result["task_id"].(string)
	if !ok || !strings.HasPrefix(taskID, "fork-") || result["status"] != "running" {
		t.Fatalf("fork launch result = %#v", result)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("fork child did not start")
	}
	stored, ok := tasks.Load(taskID)
	if !ok || stored.(Task).Status != "running" {
		t.Fatalf("task before release = %#v, found = %v", stored, ok)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case task := <-finished:
		if task.ID != taskID || task.Status != "completed" || task.Result.FinalContent != "fork result" {
			t.Fatalf("finished task = %#v", task)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fork did not report completion")
	}
	stored, ok = tasks.Load(taskID)
	if !ok || stored.(Task).Status != "completed" {
		t.Fatalf("stored task after completion = %#v, found = %v", stored, ok)
	}
}

func TestForkFailureNotification(t *testing.T) {
	var tasks sync.Map
	finished := make(chan Task, 1)
	client := stubClient{stream: func(context.Context, *llm.Request) (*conversation.Response, error) {
		return nil, errors.New("provider unavailable")
	}}
	tool := newSubAgentTool(client, &tasks)
	tool.Environment.ParentSessionProvider = func() *session.Session {
		return session.NewTransient(conversation.New())
	}
	tool.Environment.TaskFinished = func(task Task) { finished <- task }
	output, err := tool.Execute(context.Background(), map[string]any{"prompt": "fork task", "subagent_type": "fork"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	taskID := resultMap(t, output)["task_id"]
	select {
	case task := <-finished:
		if task.ID != taskID || task.Status != "failed" || !strings.Contains(task.Error, "provider unavailable") {
			t.Fatalf("failed task = %#v", task)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fork did not report failure")
	}
}
