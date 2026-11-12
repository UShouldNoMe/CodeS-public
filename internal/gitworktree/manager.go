// Package gitworktree manages owned worktrees without merging or force-removing them.
package gitworktree

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	ErrBusy    = errors.New("worktree is still in use")
	ErrChanged = errors.New("worktree contains changes or commits; preserve it and review manually")
)

type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	BaseHEAD string `json:"base_head"`
}

type Changes struct {
	// Status includes ignored and untracked files; Diff includes committed changes.
	Status  string `json:"status"`
	Diff    string `json:"diff"`
	HEAD    string `json:"head"`
	Changed bool   `json:"changed"`
}

type record struct {
	tree   Worktree
	active bool
}
type Manager struct {
	root, base string
	mu         sync.Mutex
	owned      map[string]record
}

// New is lazy: ordinary sessions do not require Git or a committed repository.
// Empty base selects the user's cache directory, outside the source repository.
func New(root, base string) *Manager {
	return &Manager{root: root, base: base, owned: make(map[string]record)}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	if ctx == nil {
		return "", errors.New("context is nil")
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)...)
	cmd.Dir = dir
	// Ignore inherited repository/index overrides. Disable checkout hooks too:
	// hooks in the source repository must not write to the parent's workspace.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(value), "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func (m *Manager) Create(ctx context.Context) (Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var tree Worktree
	top, err := git(ctx, m.root, "rev-parse", "--show-toplevel")
	if err != nil {
		return tree, err
	}
	head, err := git(ctx, m.root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return tree, err
	}
	base := m.base
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return tree, err
		}
		base = filepath.Join(cache, "CodeS", "worktrees")
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return tree, err
	}
	// Check before creating directories, and again after resolving symlinks.
	repo, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return tree, err
	}
	if within(repo, base) {
		return tree, errors.New("worktree storage must be outside source repository")
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return tree, err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return tree, err
	}
	if within(repo, base) {
		return tree, errors.New("worktree storage must be outside source repository")
	}
	container, err := os.MkdirTemp(base, "codes-")
	if err != nil {
		return tree, err
	}
	tree = Worktree{Path: filepath.Join(container, "tree"), Branch: "codes/subagent/" + rand.Text(), BaseHEAD: strings.TrimSpace(head)}
	if _, err = git(ctx, m.root, "branch", tree.Branch, tree.BaseHEAD); err != nil {
		_ = os.Remove(container)
		// The command may have committed its ref update just before being
		// cancelled. Report the branch for recovery instead of guessing and
		// deleting a possibly pre-existing conflicting ref.
		return Worktree{}, fmt.Errorf("creating branch %s for %s failed (check this ref if interrupted): %w", tree.Branch, tree.Path, err)
	}
	m.owned[tree.Path] = record{tree: tree}
	if _, err = git(ctx, m.root, "worktree", "add", "--", tree.Path, tree.Branch); err != nil {
		// Never recursively delete an uncertain/partially checked-out directory.
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rollbackErr := m.cleanup(rollbackCtx, tree); rollbackErr != nil {
			return tree, errors.Join(err, fmt.Errorf("rollback refused; retained %s (%s): %w", tree.Path, tree.Branch, rollbackErr))
		}
		return Worktree{}, err
	}
	m.owned[tree.Path] = record{tree: tree, active: true}
	return tree, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (m *Manager) ownedTree(tree Worktree) (record, error) {
	rec, ok := m.owned[tree.Path]
	if !ok || rec.tree != tree {
		return record{}, errors.New("worktree is not owned by this manager")
	}
	return rec, nil
}

func (m *Manager) inspect(ctx context.Context, tree Worktree) (Changes, error) {
	var changes Changes
	info, err := os.Lstat(tree.Path)
	if err != nil {
		return changes, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return changes, errors.New("worktree directory was replaced")
	}
	top, err := git(ctx, tree.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return changes, err
	}
	realTop, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return changes, err
	}
	if realTop != tree.Path {
		return changes, errors.New("worktree root changed")
	}
	branch, err := git(ctx, tree.Path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return changes, err
	}
	if strings.TrimSpace(branch) != "refs/heads/"+tree.Branch {
		return changes, errors.New("worktree branch changed; refusing cleanup")
	}
	status, err := git(ctx, tree.Path, "status", "--porcelain=v1", "--untracked-files=all", "--ignored", "--ignore-submodules=none")
	if err != nil {
		return changes, err
	}
	head, err := git(ctx, tree.Path, "rev-parse", "HEAD")
	if err != nil {
		return changes, err
	}
	diff, err := git(ctx, tree.Path, "diff", "--no-ext-diff", "--no-textconv", "--name-status", tree.BaseHEAD, "--")
	if err != nil {
		return changes, err
	}
	changes = Changes{Status: status, Diff: diff, HEAD: strings.TrimSpace(head)}
	changes.Changed = status != "" || changes.HEAD != tree.BaseHEAD
	return changes, nil
}

func (m *Manager) Inspect(ctx context.Context, tree Worktree) (Changes, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.ownedTree(tree); err != nil {
		return Changes{}, err
	}
	return m.inspect(ctx, tree)
}

// Finish releases the active lease even if inspection fails. Outputs survive
// completion, cancellation and application shutdown.
func (m *Manager) Finish(ctx context.Context, tree Worktree) (Changes, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, err := m.ownedTree(tree)
	if err != nil {
		return Changes{}, err
	}
	rec.active = false
	m.owned[tree.Path] = rec
	return m.inspect(ctx, tree)
}

func (m *Manager) Cleanup(ctx context.Context, tree Worktree) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanup(ctx, tree)
}

func (m *Manager) cleanup(ctx context.Context, tree Worktree) error {
	rec, err := m.ownedTree(tree)
	if err != nil {
		return err
	}
	if rec.active {
		return ErrBusy
	}
	if _, err := os.Lstat(tree.Path); err == nil {
		changes, err := m.inspect(ctx, tree)
		if err != nil {
			return err
		}
		if changes.Changed {
			return ErrChanged
		}
		// Git status can hide real edits behind assume-unchanged or
		// skip-worktree bits. Refuse those indexes rather than trusting status.
		files, err := git(ctx, tree.Path, "ls-files", "-v", "-z")
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(files, "\x00") {
			if len(entry) > 0 && (entry[0] == 'S' || (entry[0] >= 'a' && entry[0] <= 'z')) {
				return fmt.Errorf("%w: index contains hidden/skip-worktree entries", ErrChanged)
			}
		}
		if _, err := git(ctx, m.root, "worktree", "remove", "--", tree.Path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Do not remove a branch now checked out elsewhere, or silently leave a
	// stale worktree registration after an interrupted checkout.
	listing, err := git(ctx, m.root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range strings.Split(listing, "\x00") {
		if field == "branch refs/heads/"+tree.Branch {
			return errors.New("branch still has a registered worktree; retain it for manual recovery")
		}
	}
	// Compare-and-delete: never delete a branch whose tip has moved.
	if _, err := git(ctx, m.root, "update-ref", "-d", "refs/heads/"+tree.Branch, tree.BaseHEAD); err != nil {
		return err
	}
	if err := os.Remove(filepath.Dir(tree.Path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(m.owned, tree.Path)
	return nil
}
