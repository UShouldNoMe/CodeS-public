package App

import (
	"CodeS/internal/conversation"
	"CodeS/internal/gitworktree"
	"CodeS/internal/llm"
	"CodeS/internal/subAgent"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type worktreeBlockingClient struct{ started chan struct{} }

func (c *worktreeBlockingClient) Generate(context.Context, *llm.Request) (*conversation.Response, error) {
	return nil, errors.New("unexpected Generate")
}
func (c *worktreeBlockingClient) Stream(ctx context.Context, req *llm.Request, _ func(conversation.StreamEvent) error) (*conversation.Response, error) {
	last := req.Conversation.Messages[len(req.Conversation.Messages)-1]
	if last.Role != conversation.RoleTool {
		return &conversation.Response{ToolCalls: []conversation.ToolCall{{ID: "write", Name: "WriteFile", Arguments: map[string]any{"path": "result.txt", "content": "preserve"}}}}, nil
	}
	close(c.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBuildIsolationClosePreservesWorktreeAndNotification(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}, {"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	cfg := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfg, []byte("providers: []\nmcp_servers: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := Build(context.Background(), BuildOptions{WorkspaceRoot: root, ConfigPath: cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if _, err := app.NewSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	client := &worktreeBlockingClient{started: make(chan struct{})}
	app.currentClient = client
	entry, err := app.register.Get("SubAgent")
	if err != nil {
		t.Fatal(err)
	}
	tool := entry.(*subAgent.Tool)
	tool.Environment.WorktreeManager = gitworktree.New(root, t.TempDir())
	output, err := tool.Execute(context.Background(), map[string]any{"prompt": "write then wait", "isolation": true})
	if err != nil {
		t.Fatal(err)
	}
	result := output[0].(map[string]any)
	id := result["task_id"].(string)
	tree := result["worktree"].(*subAgent.WorktreeResult)
	select {
	case <-client.started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker not started")
	}
	closed := make(chan error, 1)
	go func() { closed <- app.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not wait and cancel")
	}
	value, ok := app.tasks.Load(id)
	if !ok {
		t.Fatal("task missing")
	}
	task := value.(subAgent.Task)
	if task.Status != "cancelled" || task.Worktree.CheckError != "" || !task.Worktree.Changes.Changed {
		t.Fatalf("task: %+v, metadata: %+v", task, task.Worktree)
	}
	if data, _ := os.ReadFile(filepath.Join(tree.Path, "result.txt")); string(data) != "preserve" {
		t.Fatal("Close discarded worker changes")
	}
	if _, err := os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("parent workspace changed")
	}
	found := false
	for _, message := range app.CurrentMessages() {
		if strings.Contains(message.Content, "<task_id>"+id+"</task_id>") && strings.Contains(message.Content, tree.Branch) && strings.Contains(message.Content, "result.txt") {
			found = true
		}
	}
	if !found {
		t.Fatal("shutdown failed to deliver worktree metadata")
	}
	if _, err := app.startBackgroundTask(); err == nil {
		t.Fatal("accepted task after shutdown")
	}
}
