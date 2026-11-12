package tools

import (
	"CodeS/internal/sandbox"
	"CodeS/internal/workspace"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type failingSandbox struct{ available bool }

func (s failingSandbox) Available() bool { return s.available }
func (failingSandbox) Wrap(string, sandbox.Config) (sandbox.Command, error) {
	return sandbox.Command{}, errors.New("wrap failed")
}

func TestBashFailsClosed(t *testing.T) {
	for _, available := range []bool{true, false} {
		root := t.TempDir()
		ws, err := workspace.NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		bash := NewBash(ws).WithSandbox(failingSandbox{available}, sandbox.Config{AllowWrite: []string{root}}).RequireSandbox()
		if _, err := bash.Execute(context.Background(), map[string]any{"command": "echo escaped > escaped.txt"}); err == nil {
			t.Fatal("expected sandbox error")
		}
		if _, err := os.Stat(filepath.Join(root, "escaped.txt")); !os.IsNotExist(err) {
			t.Fatal("command executed without sandbox")
		}
	}
	// A configured, available wrapper may never silently fall back, even for
	// a non-isolated caller that has not requested RequireSandbox.
	ws, _ := workspace.NewWorkspace(t.TempDir())
	_, err := NewBash(ws).WithSandbox(failingSandbox{true}, sandbox.Config{}).Execute(context.Background(), map[string]any{"command": "echo fallback"})
	if err == nil {
		t.Fatal("wrapping error ignored")
	}
}
