package subAgent

import (
	"CodeS/internal/Agent"
	"CodeS/internal/conversation"
	"CodeS/internal/gitworktree"
	"CodeS/internal/llm"
	"CodeS/internal/session"
	"CodeS/internal/tools"
	"CodeS/internal/tools/manager"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// subAgent自身的属性
type Spec struct {
	Name            string
	Description     string
	SystemPrompt    string
	AllowedTools    []string
	DisallowedTools []string
	MaxTurns        int
}

// Environment contains the runtime dependencies shared by all sub-agent roles.
// It is assembled by the application without introducing an App package dependency.
// WorktreeManager keeps filesystem ownership outside the Agent/Session packages.
type WorktreeManager interface {
	Create(context.Context) (gitworktree.Worktree, error)
	Finish(context.Context, gitworktree.Worktree) (gitworktree.Changes, error)
	Cleanup(context.Context, gitworktree.Worktree) error
}

type WorkspaceCatalogFactory func(root string) (manager.ToolCatalog, error)

type Environment struct {
	WorktreeManager         WorktreeManager
	WorkspaceCatalogFactory WorkspaceCatalogFactory
	ClientProvider          func() llm.Client
	ParentSessionProvider   func() *session.Session
	Catalog                 manager.ToolCatalog
	Permissions             *manager.PermissionManager
	Tasks                   *sync.Map              // taskID -> Task；存储值快照，不共享可变任务对象。
	TaskStart               func() (func(), error) // Registers task lifetime; returned function marks completion.
	TaskContext             context.Context        // 可选；未设置时任务随 Execute 的 ctx 取消。
	TaskFinished            func(Task)             // Delivers a terminal background task to its parent session.
}

// Tool is the single registry entry used to launch any configured sub-agent role.
type Tool struct {
	Environment Environment
	Specs       map[string]Spec
}

type WorktreeResult struct {
	gitworktree.Worktree
	Changes    gitworktree.Changes `json:"changes"`
	CheckError string              `json:"check_error,omitempty"`
}

type Task struct {
	Worktree        *WorktreeResult
	ID              string
	ParentSessionID string
	Status          string // running / completed / failed / stopped / cancelled
	Result          Agent.RunResult
	Error           string
	Cancel          context.CancelFunc
}

func (s Spec) ToPermissionPolicy() *manager.PermissionPolicy {
	ret := &manager.PermissionPolicy{
		AllowedTools:    make(map[string]struct{}),
		DisallowedTools: make(map[string]struct{}),
	}
	for _, name := range s.AllowedTools {
		ret.AllowedTools[name] = struct{}{}
	}
	for _, name := range s.DisallowedTools {
		ret.DisallowedTools[name] = struct{}{}
	}
	return ret
}

func (t *Tool) Definition() tools.ToolDef {
	return tools.ToolDef{
		Name:        "SubAgent",
		Description: "Fork runs in the background and returns a task ID immediately; its result is delivered to the parent conversation when ready. Explorer and reviewer run in the foreground and return their final result directly. Fork inherits the parent conversation; other roles start fresh, so their prompt must contain the complete task context. Internal tool calls are automatically approved.",
		Type:        "object",
		Properties: map[string]any{
			"prompt": map[string]any{ // 这是subAgent运行时候主Agent生成的prompt
				"type":        "string",
				"description": "The specific task, including scope, constraints, and expected output. For explorer and reviewer this must include all necessary background because they cannot see the parent conversation.",
			},
			"subagent_type": map[string]any{
				"type":        "string",
				"description": "Role to launch. Fork inherits the parent conversation; explorer investigates implementation; reviewer reviews an explicitly specified code scope. Defaults to fork.",
				"enum":        []string{"fork", "explorer", "reviewer"},
			},
			"isolation": map[string]any{
				"type":        "boolean",
				"description": "Create a separate Git worktree and unique branch from current HEAD. Parent uncommitted changes are not copied. The worktree is retained for review; WorktreeCleanup removes only unchanged completed worktrees.",
			},
		},
		Required:          []string{"prompt"},
		CouldBeParalleled: true,
		Authority:         tools.CategoryCommand,
	}
}

func (t *Tool) Execute(ctx context.Context, arguments map[string]any) (output []any, executeErr error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	prompt, ok := arguments["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return nil, errors.New("prompt must be a non-empty string")
	}
	mode := "fork"
	if value, exists := arguments["subagent_type"]; exists {
		var ok bool
		mode, ok = value.(string)
		if !ok || mode == "" {
			return nil, errors.New("subagent_type must be a non-empty string")
		}
	}
	spec, exists := t.Specs[mode]
	if !exists {
		return nil, fmt.Errorf("unknown subagent_type %q", mode)
	}
	isolation := false
	if value, exists := arguments["isolation"]; exists {
		var ok bool
		isolation, ok = value.(bool)
		if !ok {
			return nil, errors.New("isolation must be a boolean")
		}
	}

	env := t.Environment
	if env.ClientProvider == nil || env.Catalog == nil || env.Permissions == nil {
		return nil, fmt.Errorf("%s requires ClientProvider, Catalog and Permissions", mode)
	}
	if mode == "fork" && env.Tasks == nil {
		return nil, errors.New("fork requires Tasks")
	}
	client := env.ClientProvider()
	if client == nil {
		return nil, fmt.Errorf("%s requires a configured client", mode)
	}

	maxTurns := spec.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}
	if maxTurns <= 0 {
		return nil, fmt.Errorf("%s max turns must be greater than zero", mode)
	}

	// 所有角色都使用独立的自动批准权限管理器；Spec 限制其可见工具。
	policy := spec.ToPermissionPolicy()
	// 子 Agent 一律不能再启动子 Agent；不要用会话正文判断调用者身份。
	policy.DisallowedTools["SubAgent"] = struct{}{}
	policy.DisallowedTools["WorktreeCleanup"] = struct{}{}
	permissions, err := env.Permissions.Derive(manager.ModeFullAccess)
	if err != nil {
		return nil, err
	}
	workerConversation := conversation.New()
	if spec.SystemPrompt != "" {
		workerConversation.Messages = append(workerConversation.Messages, conversation.Message{
			Role: conversation.RoleSystem, Content: spec.SystemPrompt,
		})
	}

	input := prompt
	parentSessionID := ""
	// 如果agent类型是fork的话 那就要从父agent那里获取conversation
	if mode == "fork" {
		if env.ParentSessionProvider == nil {
			return nil, errors.New("fork requires ParentSessionProvider")
		}
		parent := env.ParentSessionProvider()
		if parent == nil {
			return nil, errors.New("fork requires an active parent session")
		}
		parentSessionID = parent.FileName()

		// Fork 使用父 Agent 实际发送给模型的上下文，保留已有压缩摘要。
		messages := parent.LLMSnapshot().Messages
		const forkMarker = "<codes_fork_worker>"
		for i := 0; i < len(messages); i++ {
			message := messages[i]
			workerConversation.Messages = append(workerConversation.Messages, message)
			if message.Role != conversation.RoleAssistant || len(message.ToolCalls) == 0 {
				continue
			}

			// 保留已有工具结果；为未完成调用补占位结果，避免 API 消息配对错误。
			answered := make(map[string]bool)
			j := i + 1
			for j < len(messages) && messages[j].Role == conversation.RoleTool {
				resultMessage := messages[j]
				if resultMessage.ToolResult == nil {
					return nil, fmt.Errorf("parent tool message %d has no result", j)
				}
				answered[resultMessage.ToolResult.ToolCallID] = true
				workerConversation.Messages = append(workerConversation.Messages, resultMessage)
				j++
			}
			for _, call := range message.ToolCalls {
				if answered[call.ID] {
					continue
				}
				result := conversation.ToolResult{
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Status:     conversation.ToolResultSuccess,
					Content:    "(tool execution interrupted by fork)",
				}
				workerConversation.AddToolResult(result)
			}
			i = j - 1
		}
		input = forkMarker + `
You are a forked worker, not the main agent.
Do not spawn sub-agents. Stay within the assigned scope.
Report your findings and any changes clearly when finished.
</codes_fork_worker>

Your task:
` + prompt
	}
	// Register the entire setup/run lifetime so Close cannot miss a checkout
	// in progress. Isolated foreground runs also participate in shutdown.
	taskDone := func() {}
	if (mode == "fork" || isolation) && env.TaskStart != nil {
		taskDone, err = env.TaskStart()
		if err != nil {
			return nil, err
		}
	}
	background := false
	defer func() {
		if !background {
			taskDone()
		}
	}()
	setupCtx, setupCancel := context.WithCancel(ctx)
	defer setupCancel()
	if isolation && env.TaskContext != nil {
		stop := context.AfterFunc(env.TaskContext, setupCancel)
		defer stop()
		if env.TaskContext.Err() != nil {
			return nil, env.TaskContext.Err()
		}
	}
	taskContext := env.TaskContext
	if taskContext == nil {
		taskContext = ctx
	}
	if err := setupCtx.Err(); err != nil {
		return nil, err
	}
	if mode == "fork" && taskContext.Err() != nil {
		return nil, taskContext.Err()
	}

	sourceCatalog := env.Catalog
	var worktree *WorktreeResult
	workerStarted := false
	if isolation {
		if env.WorktreeManager == nil || env.WorkspaceCatalogFactory == nil {
			return nil, errors.New("isolation requires WorktreeManager and WorkspaceCatalogFactory")
		}
		tree, createErr := env.WorktreeManager.Create(setupCtx)
		if createErr != nil {
			return nil, createErr
		}
		worktree = &WorktreeResult{Worktree: tree}
		// Before a worker runs, a failed setup may remove only a verified clean
		// checkout. Use an independent context so cancellation can still roll back.
		defer func() {
			if workerStarted {
				return
			}
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = env.WorktreeManager.Finish(rollbackCtx, tree)
			if cleanupErr := env.WorktreeManager.Cleanup(rollbackCtx, tree); cleanupErr != nil {
				executeErr = errors.Join(executeErr, fmt.Errorf("retained worktree %s (%s): %w", tree.Path, tree.Branch, cleanupErr))
			}
		}()
		sourceCatalog, err = env.WorkspaceCatalogFactory(tree.Path)
		if err != nil {
			return nil, err
		}
		if sourceCatalog == nil {
			return nil, errors.New("workspace factory returned a nil catalog")
		}
		input = fmt.Sprintf("Isolated workspace: %s\nBranch: %s\nAll file paths must refer to this workspace. Parent uncommitted files are absent. Git administration is read-only.\n\n%s", tree.Path, tree.Branch, input)
	}
	catalog := manager.NewFilteredCatalog(sourceCatalog, *policy)
	workerAgent := Agent.NewAgent(client, catalog, maxTurns, permissions)
	workerSession := session.NewTransient(workerConversation)
	if err := setupCtx.Err(); err != nil {
		return nil, err
	}
	if mode != "fork" {
		workerStarted = true
		task := Task{Status: "running", Worktree: worktree}
		if isolation {
			task.ID = mode + "-" + rand.Text()
		}
		result := runWorker(setupCtx, workerAgent, workerSession, input, task)
		result = finishWorktree(env, result)
		if isolation && env.Tasks != nil {
			env.Tasks.Store(result.ID, result)
		}
		response := map[string]any{"status": result.Status, "result": result.Result.FinalContent, "error": result.Error}
		if isolation {
			response["task_id"], response["worktree"] = result.ID, result.Worktree
			// Even failed/cancelled isolated runs must return their retained paths.
			return []any{response}, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if result.Status == "failed" {
			return nil, errors.New(result.Error)
		}
		return []any{response}, nil
	}
	if err := taskContext.Err(); err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(taskContext)
	taskID := mode + "-" + rand.Text()
	task := Task{
		ID: taskID, ParentSessionID: parentSessionID, Status: "running", Cancel: cancel, Worktree: worktree,
	}
	env.Tasks.Store(taskID, task)

	workerStarted, background = true, true
	go func() {
		defer taskDone()
		defer cancel()
		task = runWorker(workerCtx, workerAgent, workerSession, input, task)
		task = finishWorktree(env, task)
		env.Tasks.Store(taskID, task)
		if env.TaskFinished != nil {
			env.TaskFinished(task)
		}
	}()
	response := map[string]any{
		"task_id": taskID,
		"status":  "running",
		"message": fmt.Sprintf("%s started in the background; its result will be delivered to this conversation.", spec.Name),
	}
	if isolation {
		response["worktree"] = worktree
	}
	return []any{response}, nil
}

func runWorker(ctx context.Context, workerAgent *Agent.Agent, workerSession *session.Session, input string, task Task) Task {
	// Consume the entire stream so the agent's event producer cannot be left blocked.
	for event := range workerAgent.RunStream(ctx, workerSession, input) {
		switch e := event.(type) {
		case Agent.RunCompleted:
			task.Status = "completed"
			task.Result = e.Result
		case Agent.RunFailed:
			task.Status = "failed"
			if e.Err != nil {
				task.Error = e.Err.Error()
				if errors.Is(e.Err, context.Canceled) || errors.Is(e.Err, context.DeadlineExceeded) {
					task.Status = "cancelled"
				}
			} else {
				task.Error = "agent failed without an error"
			}
		case Agent.RunStopped:
			task.Status = "stopped"
			task.Error = e.Reason
			if ctx.Err() != nil {
				task.Status = "cancelled"
			}
		}
	}
	if task.Status == "running" {
		task.Status = "failed"
		task.Error = "agent event channel closed without a terminal event"
	}
	return task
}

// Status collection is independent of the cancelled worker context. Replace
// metadata rather than mutating the pointer held by a running Task snapshot.
func finishWorktree(env Environment, task Task) Task {
	if task.Worktree == nil {
		return task
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	metadata := *task.Worktree
	changes, err := env.WorktreeManager.Finish(ctx, metadata.Worktree)
	metadata.Changes = changes
	if err != nil {
		metadata.CheckError = err.Error()
	}
	task.Worktree = &metadata
	return task
}
