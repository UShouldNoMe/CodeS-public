package subAgent

import (
	"CodeS/internal/tools"
	"context"
	"errors"
	"sync"
)

// CleanupTool is only available to the parent, under the usual write approval.
// It accepts a known completed task, never an arbitrary filesystem path.
type CleanupTool struct {
	Tasks   *sync.Map
	Manager WorktreeManager
}

func (*CleanupTool) Definition() tools.ToolDef {
	return tools.ToolDef{
		Name:        "WorktreeCleanup",
		Description: "Explicitly remove an unchanged, finished SubAgent worktree and its branch. Refuses running tasks, modified/ignored/untracked files, and new commits. Changed worktrees must be reviewed and handled manually; there is no force option.",
		Type:        "object", Properties: map[string]any{"task_id": map[string]any{"type": "string"}},
		Required: []string{"task_id"}, Authority: tools.CategoryWrite,
	}
}

func (t *CleanupTool) Execute(ctx context.Context, args map[string]any) ([]any, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, ok := args["task_id"].(string)
	if !ok || id == "" {
		return nil, errors.New("task_id must be a non-empty string")
	}
	if t.Tasks == nil || t.Manager == nil {
		return nil, errors.New("worktree cleanup is not configured")
	}
	value, ok := t.Tasks.Load(id)
	if !ok {
		return nil, errors.New("unknown task_id")
	}
	task, ok := value.(Task)
	if !ok || task.Worktree == nil {
		return nil, errors.New("task has no isolated worktree")
	}
	if task.Status == "running" {
		return nil, errors.New("task is still running")
	}
	if err := t.Manager.Cleanup(ctx, task.Worktree.Worktree); err != nil {
		return nil, err
	}
	return []any{map[string]any{"task_id": id, "status": "cleaned", "worktree": task.Worktree.Path}}, nil
}
