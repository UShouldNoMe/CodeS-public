package tui

// 工具授权桥接：工具侧同步等待 bool，界面侧通过消息与按键异步处理。
// Approve 发送请求 → waitForApproval 交给 Update → View 展示授权内容 →
// answerApproval 将选择写入 response。App 与 TUI 必须使用同一个 broker。

import (
	"CodeS/internal/tools/manager"
	"context"

	tea "github.com/charmbracelet/bubbletea"
)

type approvalRequest struct {
	ctx      context.Context
	request  manager.Request
	response chan bool
}

// ApprovalBroker bridges the synchronous tool Approver contract to Bubble
// Tea's message-driven UI. Construct it before App.Build and pass Approve as
// BuildOptions.Approver, then pass the same broker to NewModel or Run.
type ApprovalBroker struct {
	requests chan approvalRequest
}

// 请求通道保留一个缓冲位置，每项请求另有独立结果通道。
func NewApprovalBroker() *ApprovalBroker {
	return &ApprovalBroker{requests: make(chan approvalRequest, 1)}
}

// 先发送请求，再等待回答，两个阶段均响应上下文取消。
// 结果通道带缓冲，工具取消等待后，界面仍可写回该请求的选择。
func (b *ApprovalBroker) Approve(ctx context.Context, request manager.Request) (bool, error) {
	if b == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pending := approvalRequest{
		ctx:      ctx,
		request:  request,
		response: make(chan bool, 1),
	}
	select {
	case b.requests <- pending:
	case <-ctx.Done():
		return false, ctx.Err()
	}

	select {
	case allowed := <-pending.response:
		return allowed, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

type approvalRequestMsg struct {
	request approvalRequest
}

// 在 Cmd 中等待请求，避免阻塞 Update；没有 broker 时不创建监听任务。
func waitForApproval(broker *ApprovalBroker) tea.Cmd {
	if broker == nil {
		return nil
	}
	return func() tea.Msg {
		return approvalRequestMsg{request: <-broker.requests}
	}
}
