package subAgent

import (
	"CodeS/internal/conversation"
	"CodeS/internal/gitworktree"
	"CodeS/internal/llm"
	"CodeS/internal/session"
	"CodeS/internal/tools/manager"
	"CodeS/internal/workspacecatalog"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func isolationRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git unavailable")
	}
	root := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	git("init")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "base")
	return root
}

func isolatedTool(t *testing.T, root string, client llm.Client) *Tool {
	t.Helper()
	tool := newSubAgentTool(client, &sync.Map{})
	parent := session.NewTransient(conversation.New())
	tool.Environment.ParentSessionProvider = func() *session.Session { return parent }
	catalog, err := workspacecatalog.New(root, false)
	if err != nil {
		t.Fatal(err)
	}
	tool.Environment.Catalog = catalog
	tool.Environment.WorktreeManager = gitworktree.New(root, t.TempDir())
	tool.Environment.WorkspaceCatalogFactory = func(root string) (manager.ToolCatalog, error) { return workspacecatalog.New(root, true) }
	return tool
}

func writingClient(t *testing.T, afterWrite func(context.Context)) llm.Client {
	return stubClient{stream: func(ctx context.Context, req *llm.Request) (*conversation.Response, error) {
		for _, def := range req.Tools {
			if def.Name == "SubAgent" || def.Name == "WorktreeCleanup" {
				t.Error("child management tool exposed")
			}
		}
		last := req.Conversation.Messages[len(req.Conversation.Messages)-1]
		if last.Role == conversation.RoleTool {
			if last.ToolResult.Status != conversation.ToolResultSuccess {
				t.Errorf("write result: %+v", last.ToolResult)
			}
			if afterWrite != nil {
				afterWrite(ctx)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return &conversation.Response{Content: "done"}, nil
		}
		return &conversation.Response{ToolCalls: []conversation.ToolCall{{ID: "write", Name: "WriteFile", Arguments: map[string]any{"path": "tracked.txt", "content": "worker"}}}}, nil
	}}
}

func TestIsolatedConcurrentForkWrites(t *testing.T) {
	root := isolationRepo(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("parent dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	tool := isolatedTool(t, root, writingClient(t, nil))
	finished := make(chan Task, 2)
	tool.Environment.TaskFinished = func(task Task) { finished <- task }
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			output, err := tool.Execute(context.Background(), map[string]any{"prompt": "write", "isolation": true})
			if err != nil {
				t.Error(err)
				return
			}
			if resultMap(t, output)["worktree"] == nil {
				t.Error("launch metadata missing")
			}
		})
	}
	wg.Wait()
	paths, branches := map[string]bool{}, map[string]bool{}
	for range 2 {
		select {
		case task := <-finished:
			if task.Status != "completed" || task.Worktree == nil || task.Worktree.CheckError != "" || !task.Worktree.Changes.Changed {
				t.Fatalf("result: %+v / %+v", task, task.Worktree)
			}
			tree := task.Worktree
			if paths[tree.Path] || branches[tree.Branch] {
				t.Fatal("shared workspace")
			}
			paths[tree.Path], branches[tree.Branch] = true, true
			data, err := os.ReadFile(filepath.Join(tree.Path, "tracked.txt"))
			if err != nil || string(data) != "worker" {
				t.Fatalf("worker output: %q %v", data, err)
			}
			cleanup := CleanupTool{Tasks: tool.Environment.Tasks, Manager: tool.Environment.WorktreeManager}
			if _, err := cleanup.Execute(context.Background(), map[string]any{"task_id": task.ID}); !errors.Is(err, gitworktree.ErrChanged) {
				t.Fatalf("changed cleanup = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("worker timed out")
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "tracked.txt")); string(data) != "parent dirty" {
		t.Fatalf("parent modified: %q", data)
	}
}

func TestIsolatedReadOnlyRolesRejectWritesAndRecursion(t *testing.T) {
	for _, role := range []string{"explorer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			root := isolationRepo(t)
			calls := 0
			client := stubClient{stream: func(_ context.Context, req *llm.Request) (*conversation.Response, error) {
				for _, def := range req.Tools {
					if def.Name != "ReadFile" && def.Name != "ListFile" && def.Name != "Grep" && def.Name != "Glob" {
						t.Errorf("forbidden definition: %s", def.Name)
					}
				}
				calls++
				if calls == 1 {
					return &conversation.Response{ToolCalls: []conversation.ToolCall{
						{ID: "write", Name: "WriteFile", Arguments: map[string]any{"path": "bad.txt", "content": "bad"}},
						{ID: "recursive", Name: "SubAgent", Arguments: map[string]any{"prompt": "recurse"}},
						{ID: "shell", Name: "Bash", Arguments: map[string]any{"command": "echo bad > bad.txt"}},
					}}, nil
				}
				for _, message := range req.Conversation.Messages {
					if message.ToolResult != nil && message.ToolResult.Status == conversation.ToolResultSuccess {
						t.Error("forbidden tool executed")
					}
				}
				return &conversation.Response{Content: "reviewed"}, nil
			}}
			tool := isolatedTool(t, root, client)
			output, err := tool.Execute(context.Background(), map[string]any{"prompt": "inspect", "subagent_type": role, "isolation": true})
			if err != nil {
				t.Fatal(err)
			}
			result := resultMap(t, output)
			tree := result["worktree"].(*WorktreeResult)
			if tree.Changes.Changed || tree.CheckError != "" {
				t.Fatalf("read-only changes: %+v", tree)
			}
			cleanup := CleanupTool{Tasks: tool.Environment.Tasks, Manager: tool.Environment.WorktreeManager}
			if _, err := cleanup.Execute(context.Background(), map[string]any{"task_id": result["task_id"]}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIsolationFactoryFailureRollsBack(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "dirty"}[dirty], func(t *testing.T) {
			root := isolationRepo(t)
			client := stubClient{stream: func(context.Context, *llm.Request) (*conversation.Response, error) {
				t.Error("worker created before factory succeeded")
				return nil, errors.New("unexpected")
			}}
			tool := isolatedTool(t, root, client)
			var path string
			tool.Environment.WorkspaceCatalogFactory = func(root string) (manager.ToolCatalog, error) {
				path = root
				if dirty {
					if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return nil, errors.New("factory failed")
			}
			_, err := tool.Execute(context.Background(), map[string]any{"prompt": "run", "isolation": true})
			if err == nil || !strings.Contains(err.Error(), "factory failed") {
				t.Fatalf("error = %v", err)
			}
			_, statErr := os.Stat(path)
			if !dirty && !os.IsNotExist(statErr) {
				t.Fatalf("rollback left checkout: %v", statErr)
			}
			if dirty && (statErr != nil || !strings.Contains(err.Error(), path)) {
				t.Fatalf("dirty checkout lost or not reported: %v", err)
			}
		})
	}
}

func TestIsolationShutdownCancelsAndPreserves(t *testing.T) {
	root := isolationRepo(t)
	started := make(chan struct{})
	tool := isolatedTool(t, root, writingClient(t, func(ctx context.Context) { close(started); <-ctx.Done() }))
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool.Environment.TaskContext = lifetime
	var wg sync.WaitGroup
	tool.Environment.TaskStart = func() (func(), error) { wg.Add(1); return wg.Done, nil }
	finished := make(chan Task, 1)
	tool.Environment.TaskFinished = func(task Task) { finished <- task }
	output, err := tool.Execute(context.Background(), map[string]any{"prompt": "write and wait", "isolation": true})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := CleanupTool{Tasks: tool.Environment.Tasks, Manager: tool.Environment.WorktreeManager}
	if _, err := cleanup.Execute(context.Background(), map[string]any{"task_id": resultMap(t, output)["task_id"]}); err == nil {
		t.Fatal("running task cleaned")
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("worker not started")
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not drain task")
	}
	task := <-finished
	if task.Status != "cancelled" || task.Worktree == nil || !task.Worktree.Changes.Changed {
		t.Fatalf("cancelled task: %+v", task)
	}
	if data, _ := os.ReadFile(filepath.Join(task.Worktree.Path, "tracked.txt")); string(data) != "worker" {
		t.Fatal("cancelled work lost")
	}
}

func TestNonIsolatedForkKeepsParentWorkspace(t *testing.T) {
	root := isolationRepo(t)
	tool := isolatedTool(t, root, writingClient(t, nil))
	tool.Environment.WorkspaceCatalogFactory = func(string) (manager.ToolCatalog, error) {
		t.Error("factory called without isolation")
		return nil, errors.New("unexpected")
	}
	finished := make(chan Task, 1)
	tool.Environment.TaskFinished = func(task Task) { finished <- task }
	if _, err := tool.Execute(context.Background(), map[string]any{"prompt": "write", "isolation": false}); err != nil {
		t.Fatal(err)
	}
	select {
	case task := <-finished:
		if task.Worktree != nil {
			t.Fatal("unexpected worktree")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker timed out")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "tracked.txt")); string(data) != "worker" {
		t.Fatal("non-isolated binding changed")
	}
}
