package tui

// 本包阅读入口：采用 Bubble Tea 的 Model / Update / View 模式组织终端界面。
// 启动顺序：main 构建 App 与授权桥 → Run → NewModel → Init。
// 键盘、窗口变化和后台结果作为消息进入 Update；Update 修改状态并返回后续 Cmd；
// View 根据状态生成整帧字符串，由框架渲染。业务请求、工具和持久化由 App 管理。
// 建议阅读顺序：model.go → messages.go → update.go → view.go → 各功能文件。

import (
	"CodeS/internal/Agent"
	"CodeS/internal/App"
	"CodeS/internal/conversation"
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

type entryKind string

const (
	entryUser      entryKind = "user"
	entryAssistant entryKind = "assistant"
	entryTool      entryKind = "tool"
	entrySystem    entryKind = "system"
	entryError     entryKind = "error"
)

type transcriptEntry struct {
	kind    entryKind
	content string
}

// Model contains UI state and the CodeS application. It deliberately does not
// own the LLM client, session persistent, tool registry, or MCP manager separately.
type Model struct {
	// 基础依赖：上下文传递取消信号，App 提供业务能力，approvals 接收工具授权请求。
	ctx       context.Context //传递信号所用
	app       *App.App
	approvals *ApprovalBroker //

	// 显示数据：input 是未提交输入；transcript 是本地显示记录，不等同于模型上下文。
	input            string
	commandSelection int
	commandDismissed bool
	transcript       []transcriptEntry // 已经有过的聊天记录
	stream           string            //正在流式生成的内容
	status           string            // 底部状态栏显示的文本
	// Usage covers each visit to a session during this TUI run. Completed visits
	// are kept until the alternate screen closes so they can be printed afterward.
	sessionUsage          conversation.Usage
	completedSessionUsage []conversation.Usage

	// 最近一次尺寸消息记录的终端列数和行数，供布局使用。
	width  int
	height int
	// scrollTop is the first visible transcript line when followBottom is false.
	scrollTop    int
	followBottom bool

	// 当前 Agent 运行：通道负责接收事件，取消函数用于中止本轮请求。
	running   bool
	agentCh   <-chan Agent.Event
	cancelRun context.CancelFunc

	pendingApproval  *approvalRequest // 权限弹窗状态
	shownToolResults map[string]bool  // 按工具调用 ID 记录结果是否已显示，不是工具执行历史。
	quitting         bool
	// sessionBusy 表示会话操作进行中；quitAfterSession 要求等该操作结束再退出。
	sessionBusy      bool
	quitAfterSession bool
	// exitPending 表示已安排延时退出；quitting 表示 View 应停止生成普通画面。
	exitPending bool
	// leavingUsage 保存切换前用量；两个 picker 非空时分别代表菜单已打开。
	leavingUsage *conversation.Usage
	resumePicker *resumePicker
	modelPicker  *modelPicker
	inputCursor  *inputCursorWriter
}

// 初始化界面状态并检查 App；此时尚不创建会话，创建任务由 Init 发起。
func NewModel(ctx context.Context, application *App.App, approvals *ApprovalBroker) (*Model, error) {
	if application == nil {
		return nil, errors.New("TUI app is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &Model{
		ctx:              ctx,
		app:              application,
		approvals:        approvals,
		status:           "ready",
		followBottom:     true,
		shownToolResults: make(map[string]bool),
	}, nil
}

// 启动时禁止提交输入，并行发起新建会话任务和授权监听。
func (m *Model) Init() tea.Cmd {
	m.sessionBusy = true
	m.status = "creating session"
	return tea.Batch(createSession(m.ctx, m.app), waitForApproval(m.approvals))
}

// Run uses the alternate screen for the full-window UI, then prints session
// usage in the normal terminal buffer after Bubble Tea restores it. The
// ApprovalBroker must be the same instance used as App.BuildOptions.Approver.
// 输出经过光标包装器；App 的关闭由调用方 main 负责。
func Run(ctx context.Context, application *App.App, approvals *ApprovalBroker) error {
	model, err := NewModel(ctx, application, approvals)
	if err != nil {
		return err
	}
	// Bubble Tea keeps its renderer cursor at the end of the rendered frame.
	// The wrapper moves it to the prompt while the user is typing, which also
	// gives Windows IMEs the correct place to draw their pre-edit text.
	model.inputCursor = newInputCursorWriter(os.Stdout, true)
	_, err = tea.NewProgram(
		model,
		tea.WithContext(model.ctx),
		tea.WithOutput(model.inputCursor),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	).Run()
	for index, usage := range model.completedSessionUsage {
		_, _ = fmt.Fprintf(os.Stdout, "session %d: %s\n", index+1, sessionUsageStatus(usage))
	}
	if model.app.CurrentSessionFileName() != "" {
		_, _ = fmt.Fprintf(os.Stdout, "session %d: %s\n", len(model.completedSessionUsage)+1, sessionUsageStatus(model.sessionUsage))
	}
	return err
}
