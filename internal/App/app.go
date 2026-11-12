package App

import (
	"CodeS/config"
	"CodeS/internal/Agent"
	"CodeS/internal/MCP"
	"CodeS/internal/compact"
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/persistent"
	"CodeS/internal/session"
	"CodeS/internal/subAgent"
	"CodeS/internal/tools/manager"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// App应该一并管理Session 后台持久化组件连接的开关 以及切换Session的入口

type App struct {
	permissions    *manager.PermissionManager
	tasks          sync.Map
	taskContext    context.Context
	taskCancel     context.CancelFunc
	taskMu         sync.Mutex
	taskWG         sync.WaitGroup
	notificationMu sync.Mutex
	notifications  []subAgent.Task
	closing        bool
	agent          *Agent.Agent
	currentClient  llm.Client
	currentSession *session.Session
	currentModel   *llm.ModelInfo
	store          persistent.Store
	closeStore     func()
	closeOnce      sync.Once
	closeErr       error
	mcpManager     *MCP.Manager
	register       *manager.Manager
	providers      []config.ProviderConfig
	models         []llm.ModelInfo
	modelMu        sync.Mutex
}

func New(agent *Agent.Agent, currentStore persistent.Store, manager *MCP.Manager, reg *manager.Manager) (*App, error) {
	if agent == nil {
		return nil, errors.New("agent is nil")
	}
	if currentStore == nil {
		return nil, errors.New("persistent is nil")
	}
	if manager == nil {
		return nil, errors.New("mcp manager is nil")
	}
	if reg == nil {
		return nil, errors.New("registry is nil")
	}
	application := &App{
		agent:          agent,
		currentSession: nil,
		currentModel:   nil,
		store:          currentStore,
		mcpManager:     manager,
		register:       reg,
		providers:      []config.ProviderConfig{},
		models:         []llm.ModelInfo{},
	}
	if closer, ok := currentStore.(interface{ Close() }); ok {
		application.closeStore = closer.Close
	}
	return application, nil
}

func (a *App) ChatStream(ctx context.Context, input string) <-chan Agent.Event {
	return a.agent.RunStream(ctx, a.currentSession, input)
}

func (a *App) SwitchSession(ctx context.Context, fileName string) error {
	if a.currentSession != nil && a.currentSession.FileName() == fileName {
		return nil
	}
	nextSession, err := session.OpenSession(ctx, fileName, a.store)
	if err != nil {
		return err
	}
	if err := a.finishCurrentSession(ctx); err != nil {
		return err
	}
	a.currentSession = nextSession
	return nil
}

func (a *App) NewSession(ctx context.Context) (string, error) {
	nextSession, err := session.NewSession(ctx, a.store)
	if err != nil {
		return "", err
	}
	if err := a.finishCurrentSession(ctx); err != nil {
		return "", errors.Join(err, a.store.DeleteSession(ctx, nextSession.FileName()))
	}
	a.currentSession = nextSession
	return nextSession.FileName(), nil
}

func (a *App) ListSessions(ctx context.Context) ([]persistent.SessionInfo, error) {
	return a.store.ListSessions(ctx)
}

func (a *App) CurrentSessionFileName() string {
	if a.currentSession == nil {
		return ""
	}
	return a.currentSession.FileName()
}

// CurrentMessages returns a snapshot of the current session conversation.
// UI layers can use the snapshot to rebuild their visible history after a
// session switch without gaining direct access to the mutable session.
func (a *App) CurrentMessages() []conversation.Message {
	if a.currentSession == nil {
		return nil
	}
	return a.currentSession.Snapshot().Messages
}

func (a *App) ClearCurrentSession() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.finishCurrentSession(ctx); err != nil {
		return err
	}
	a.currentSession = nil
	return nil
}

// finishCurrentSession removes an empty session or records its exit timestamp.
func (a *App) finishCurrentSession(ctx context.Context) error {
	if a.currentSession == nil {
		return nil
	}
	messages, err := a.store.LoadMessages(ctx, a.currentSession.FileName())
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return a.store.DeleteSession(ctx, a.currentSession.FileName())
	}
	return a.store.TouchSession(ctx, a.currentSession.FileName())
}

func (a *App) DeleteSession(ctx context.Context, fileName string) error {
	if err := a.store.DeleteSession(ctx, fileName); err != nil {
		return err
	}
	if a.currentSession != nil && a.currentSession.FileName() == fileName {
		a.currentSession = nil
	}
	return nil
}

func (a *App) Close() error {
	a.closeOnce.Do(func() {
		a.taskMu.Lock()
		a.closing = true
		if a.taskCancel != nil {
			a.taskCancel()
		}
		a.taskMu.Unlock()
		a.taskWG.Wait()
		// The UI context may already be cancelled on Ctrl+C. Use an independent
		// bounded context so shutdown can still persist the last exit timestamp.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.closeErr = errors.Join(a.deliverTaskNotifications(ctx, a.currentSession), a.finishCurrentSession(ctx))
		if a.closeStore != nil {
			a.closeStore()
		}
		a.mcpManager.CloseAll()

	})
	return a.closeErr
}

func (a *App) McpConnect(ctx context.Context) error {
	return a.mcpManager.ConnectAll(ctx)
}

func (a *App) RegisterAllMCPTools() error {
	mcpTools := a.mcpManager.GetTools()
	var errs error
	for _, tool := range mcpTools {
		err := a.register.Register(tool)
		if err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

func (a *App) CompactContext(ctx context.Context) error {
	if a.currentSession == nil {
		return errors.New("no active session")
	}
	formattedMessages := compact.FormatMessage(a.currentSession.LLMSnapshot())
	if formattedMessages == "" {
		return errors.New("conversation is empty")
	}
	compactedMessage, err := a.agent.Compact(ctx, formattedMessages)
	if err != nil {
		return err
	}
	return a.currentSession.ApplyCompaction(ctx, compactedMessage)
}

func (a *App) GetCurrentClient() llm.Client {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	return a.currentClient
}

func (a *App) ExecutionMode() manager.ExecutionMode {
	if a.permissions == nil {
		return manager.ModeManual
	}
	return a.permissions.ExecutionMode()
}
func (a *App) SetExecutionMode(mode manager.ExecutionMode) error {
	if a.permissions == nil {
		return errors.New("permission manager is not configured")
	}
	return a.permissions.SetExecutionMode(mode)
}
func (a *App) startBackgroundTask() (func(), error) {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	if a.closing {
		return nil, errors.New("application is closing")
	}
	a.taskWG.Add(1)
	return a.taskWG.Done, nil
}

func (a *App) enqueueTaskNotification(task subAgent.Task) {
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	a.notifications = append(a.notifications, task)
}

func (a *App) deliverTaskNotifications(ctx context.Context, currentSession *session.Session) error {
	if currentSession == nil {
		return nil
	}
	a.notificationMu.Lock()
	defer a.notificationMu.Unlock()
	for i := 0; i < len(a.notifications); {
		task := a.notifications[i]
		if task.ParentSessionID != currentSession.FileName() {
			i++
			continue
		}
		content := task.Result.FinalContent
		if task.Status != "completed" {
			content = task.Error
		}
		notification := fmt.Sprintf("<task-notification>\n<task_id>%s</task_id>\n<status>%s</status>\n<result>%s</result>\n</task-notification>", task.ID, task.Status, content)
		if task.Worktree != nil {
			metadata, err := json.Marshal(task.Worktree)
			if err != nil {
				return err
			}
			notification += "\n<worktree>" + string(metadata) + "</worktree>"
		}
		if err := currentSession.AddSystem(ctx, notification); err != nil {
			return fmt.Errorf("deliver task %s: %w", task.ID, err)
		}
		a.notifications = append(a.notifications[:i], a.notifications[i+1:]...)
	}
	return nil
}
