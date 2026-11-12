package workspacecatalog

import (
	"CodeS/internal/sandbox"
	"CodeS/internal/tools"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestIndependentCatalogPaths(t *testing.T) {
	main := t.TempDir()
	child := t.TempDir()
	catalog, err := New(child, true)
	if err != nil {
		t.Fatal(err)
	}
	write, _ := catalog.Get("WriteFile")
	for _, path := range []string{filepath.Join(main, "escape.txt"), "../escape.txt", ".git", ".git/config"} {
		if _, err := write.Execute(context.Background(), map[string]any{"path": path, "content": "escape"}); err == nil {
			t.Fatalf("accepted path %s", path)
		}
	}
	if err := os.Symlink(main, filepath.Join(child, "link")); err == nil {
		if _, err := write.Execute(context.Background(), map[string]any{"path": "link/escape.txt", "content": "escape"}); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
	if _, err := write.Execute(context.Background(), map[string]any{"path": "result.txt", "content": "worker"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(main, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("parent written")
	}
	if _, err := catalog.Get("SubAgent"); err == nil {
		t.Fatal("recursive SubAgent exposed")
	}
}

func TestSandboxWorktreeWriteBoundary(t *testing.T) {
	backend := sandbox.New()
	if backend == nil || !backend.Available() {
		t.Skip("OS sandbox unavailable")
	}
	if runtime.GOOS == "windows" {
		t.Skip("no Windows sandbox backend")
	}
	parent, sibling, child := t.TempDir(), t.TempDir(), t.TempDir()
	catalog, err := New(child, true)
	if err != nil {
		t.Fatal(err)
	}
	bash, _ := catalog.Get("Bash")
	// Probe only the backend startup; once available, boundary failures are real.
	output, err := bash.Execute(context.Background(), map[string]any{"command": "true"})
	if err != nil {
		t.Skipf("sandbox cannot start: %v", err)
	}
	if result := output[0].(tools.BashResult); result.ExitCode != 0 {
		t.Skipf("sandbox cannot start: %s", result.Stderr)
	}
	if err := os.WriteFile(filepath.Join(child, ".git"), []byte("metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	command := "printf worker > result.txt; printf escape > '" + filepath.ToSlash(filepath.Join(parent, "escape.txt")) + "'; printf escape > '" + filepath.ToSlash(filepath.Join(sibling, "escape.txt")) + "'; printf escape > .git"
	output, err = bash.Execute(context.Background(), map[string]any{"command": command})
	if err != nil {
		t.Fatal(err)
	}
	if output[0].(tools.BashResult).ExitCode == 0 {
		t.Fatal("protected write succeeded")
	}
	if data, err := os.ReadFile(filepath.Join(child, "result.txt")); err != nil || string(data) != "worker" {
		t.Fatalf("worktree cwd/write failed: %q %v", data, err)
	}
	for _, root := range []string{parent, sibling} {
		if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
			t.Fatalf("outside write: %s", root)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(child, ".git")); string(data) != "metadata" {
		t.Fatal("Git metadata modified")
	}
}

// Cancellation must finish the whole sandbox, including shell descendants,
// before the SubAgent can release its worktree lease.
func TestSandboxCancellationStopsChildren(t *testing.T) {
	backend := sandbox.New()
	if backend == nil || !backend.Available() {
		t.Skip("OS sandbox unavailable")
	}
	root := t.TempDir()
	catalog, err := New(root, true)
	if err != nil {
		t.Fatal(err)
	}
	bash, _ := catalog.Get("Bash")
	probe, err := bash.Execute(context.Background(), map[string]any{"command": "true"})
	if err != nil {
		t.Skipf("sandbox cannot start: %v", err)
	}
	if result := probe[0].(tools.BashResult); result.ExitCode != 0 {
		t.Skipf("sandbox cannot start: %s", result.Stderr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := bash.Execute(ctx, map[string]any{"command": "(sleep 1; printf escaped > survivor.txt) & printf ready > ready.txt; wait"})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "ready.txt")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("shell exited before cancellation: %v", err)
		case <-deadline:
			t.Fatal("shell did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shell did not stop")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "survivor.txt")); !os.IsNotExist(err) {
		t.Fatal("shell descendant survived cancellation")
	}
}
