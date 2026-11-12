package App

import (
	"CodeS/config"
	"CodeS/internal/Agent"
	"CodeS/internal/MCP"
	"CodeS/internal/gitworktree"
	"CodeS/internal/persistent"
	"CodeS/internal/session"
	"CodeS/internal/subAgent"
	"CodeS/internal/tools/manager"
	"CodeS/internal/workspace"
	"CodeS/internal/workspacecatalog"
	"context"
	"fmt"
	"path/filepath"
)

type BuildOptions struct {
	WorkspaceRoot string
	MaxTurns      int
	Approver      manager.Approver
	ConfigPath    string
}

func Build(ctx context.Context, options BuildOptions) (*App, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	if options.WorkspaceRoot == "" {
		options.WorkspaceRoot = "."
	}
	if options.MaxTurns <= 0 {
		options.MaxTurns = 10
	}

	cfg, err := config.Load(options.ConfigPath)
	if err != nil {
		return nil, err
	}
	mode := manager.ExecutionMode(cfg.ExecutionMode)
	if mode == "" {
		mode = manager.ModeManual
	}
	permissionManager := manager.New(options.Approver)
	if err := permissionManager.SetExecutionMode(mode); err != nil {
		return nil, err
	}

	ws, err := workspace.NewWorkspace(options.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	registry, err := workspacecatalog.New(ws.Root(), false)
	if err != nil {
		return nil, err
	}
	worktrees := gitworktree.New(ws.Root(), "")

	currentStore, err := persistent.JSONLStoreNew(filepath.Join(ws.Root(), ".codes", "sessions"))
	if err != nil {
		return nil, err
	}

	mcpManager, err := MCP.NewManager(cfg.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("build mcp manager failed with err: %w", err)
	}

	// The concrete LLM client is created only after the user selects a
	// provider and model. Until then the Agent remains unconfigured.
	agent := Agent.NewAgent(nil, registry, options.MaxTurns, permissionManager)

	application, err := New(agent, currentStore, mcpManager, registry)
	if err != nil {
		return nil, err
	}
	application.permissions = permissionManager
	application.taskContext, application.taskCancel = context.WithCancel(ctx)
	agent.BeforeRequest = application.deliverTaskNotifications
	subAgentTool := &subAgent.Tool{
		Specs: subAgent.BuiltinSpecs,
		Environment: subAgent.Environment{
			ClientProvider:          application.GetCurrentClient,
			WorktreeManager:         worktrees,
			WorkspaceCatalogFactory: func(root string) (manager.ToolCatalog, error) { return workspacecatalog.New(root, true) },
			ParentSessionProvider:   func() *session.Session { return application.currentSession },
			Catalog:                 registry,
			Permissions:             permissionManager,
			Tasks:                   &application.tasks,
			TaskContext:             application.taskContext,
			TaskStart:               application.startBackgroundTask,
			TaskFinished:            application.enqueueTaskNotification,
		},
	}
	if err := registry.Register(subAgentTool); err != nil {
		_ = application.Close()
		return nil, fmt.Errorf("register subagent: %w", err)
	}
	if err := registry.Register(&subAgent.CleanupTool{Tasks: &application.tasks, Manager: worktrees}); err != nil {
		_ = application.Close()
		return nil, err
	}
	application.setProviders(cfg.Providers)
	return application, nil
}

func (a *App) setProviders(providers []config.ProviderConfig) {
	a.providers = append([]config.ProviderConfig(nil), providers...)
}
