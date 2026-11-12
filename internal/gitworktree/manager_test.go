package gitworktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func testRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("system Git unavailable")
	}
	root := t.TempDir()
	testGit(t, root, "init")
	testGit(t, root, "config", "user.name", "CodeS Test")
	testGit(t, root, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("HEAD\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "base")
	return root
}

func TestConcurrentWorktreesAndCleanRemoval(t *testing.T) {
	root := testRepo(t)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("parent dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := testGit(t, root, "status", "--porcelain")
	manager := New(root, t.TempDir())
	const count = 4
	trees := make([]Worktree, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() { trees[i], errs[i] = manager.Create(context.Background()) })
	}
	wg.Wait()
	paths, branches := map[string]bool{}, map[string]bool{}
	for i, tree := range trees {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if paths[tree.Path] || branches[tree.Branch] {
			t.Fatal("worktrees overlap")
		}
		paths[tree.Path], branches[tree.Branch] = true, true
		data, err := os.ReadFile(filepath.Join(tree.Path, "tracked.txt"))
		if err != nil || string(data) != "HEAD\n" {
			t.Fatalf("checkout = %q, %v", data, err)
		}
		if err := manager.Cleanup(context.Background(), tree); !errors.Is(err, ErrBusy) {
			t.Fatalf("active cleanup = %v", err)
		}
		changes, err := manager.Finish(context.Background(), tree)
		if err != nil || changes.Changed {
			t.Fatalf("finish = %+v, %v", changes, err)
		}
		if err := manager.Cleanup(context.Background(), tree); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(tree.Path); !os.IsNotExist(err) {
			t.Fatalf("worktree remains: %v", err)
		}
		if _, err := git(context.Background(), root, "show-ref", "--verify", "refs/heads/"+tree.Branch); err == nil {
			t.Fatal("branch remains")
		}
	}
	if after := testGit(t, root, "status", "--porcelain"); after != before {
		t.Fatalf("parent status changed: %s", after)
	}
}

func TestCleanupPreservesAllChanges(t *testing.T) {
	for _, kind := range []string{"modified", "staged", "untracked", "ignored", "committed"} {
		t.Run(kind, func(t *testing.T) {
			root := testRepo(t)
			manager := New(root, t.TempDir())
			tree, err := manager.Create(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			name := "tracked.txt"
			if kind == "untracked" {
				name = "new.txt"
			}
			if kind == "ignored" {
				name = "ignored.txt"
			}
			if err := os.WriteFile(filepath.Join(tree.Path, name), []byte("worker\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" || kind == "committed" {
				testGit(t, tree.Path, "add", name)
			}
			if kind == "committed" {
				testGit(t, tree.Path, "commit", "-m", "worker result")
			}
			changes, err := manager.Finish(context.Background(), tree)
			if err != nil || !changes.Changed {
				t.Fatalf("finish = %+v, %v", changes, err)
			}
			if err := manager.Cleanup(context.Background(), tree); !errors.Is(err, ErrChanged) {
				t.Fatalf("cleanup = %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(tree.Path, name)); err != nil || string(data) != "worker\n" {
				t.Fatalf("lost output: %q %v", data, err)
			}
		})
	}
}

func TestInvalidCreationAndOwnership(t *testing.T) {
	root := testRepo(t)
	t.Run("missing git", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := New(root, t.TempDir()).Create(context.Background()); err == nil {
			t.Fatal("expected missing Git error")
		}
	})
	t.Run("non repository", func(t *testing.T) {
		if _, err := New(t.TempDir(), t.TempDir()).Create(context.Background()); err == nil {
			t.Fatal("expected repository error")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := New(root, t.TempDir()).Create(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("directory conflict", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(root, path).Create(context.Background()); err == nil {
			t.Fatal("expected collision error")
		}
		data, _ := os.ReadFile(path)
		if string(data) != "keep" {
			t.Fatal("conflicting path modified")
		}
	})
	t.Run("storage inside source", func(t *testing.T) {
		if _, err := New(root, filepath.Join(root, "trees")).Create(context.Background()); err == nil {
			t.Fatal("expected storage error")
		}
	})
	t.Run("unowned", func(t *testing.T) {
		if err := New(root, t.TempDir()).Cleanup(context.Background(), Worktree{Path: root}); err == nil {
			t.Fatal("expected ownership error")
		}
	})
}

func TestCreateFailureRollsBackBranch(t *testing.T) {
	root := testRepo(t)
	base := t.TempDir()
	manager := New(root, base)
	// A required smudge filter deterministically fails during checkout.
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("tracked.txt filter=fail\n"), 0600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".gitattributes")
	testGit(t, root, "commit", "-m", "filter")
	testGit(t, root, "config", "filter.fail.smudge", "nonexistent-codes-filter-command")
	testGit(t, root, "config", "filter.fail.required", "true")
	tree, err := manager.Create(context.Background())
	if err == nil {
		t.Fatal("expected checkout failure")
	}
	// A partial checkout may contain data: preserving it with an explicit path
	// is safer than force-removing it. Otherwise there must be no orphan branch.
	branches := testGit(t, root, "for-each-ref", "--format=%(refname)", "refs/heads/codes/subagent/")
	if tree.Path == "" && branches != "" {
		t.Fatalf("orphan branch: %s", branches)
	}
	if tree.Path != "" && !strings.Contains(err.Error(), tree.Path) {
		t.Fatalf("retained path missing: %v", err)
	}
}

func TestCleanupRefusesHiddenChanges(t *testing.T) {
	root := testRepo(t)
	manager := New(root, t.TempDir())
	tree, err := manager.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	testGit(t, tree.Path, "update-index", "--assume-unchanged", "tracked.txt")
	if err := os.WriteFile(filepath.Join(tree.Path, "tracked.txt"), []byte("hidden change"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Finish(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if err := manager.Cleanup(context.Background(), tree); !errors.Is(err, ErrChanged) {
		t.Fatalf("hidden changes cleanup: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(tree.Path, "tracked.txt")); string(data) != "hidden change" {
		t.Fatal("hidden changes lost")
	}
}
