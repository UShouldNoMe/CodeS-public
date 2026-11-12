package tui

// 异步任务与消息协议：tea.Cmd 是由框架执行的函数，返回值为 tea.Msg。
// 任务调用 App 或等待通道，不直接修改 Model；结果回到 Update 后才更新界面。
// Agent 事件每次读取一条，处理后继续订阅，形成持续接收链路。

import (
	"CodeS/internal/Agent"
	"CodeS/internal/App"
	"CodeS/internal/conversation"
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type agentStartedMsg struct {
	ch  <-chan Agent.Event
	err error
}

type agentEventMsg struct {
	event Agent.Event
	ch    <-chan Agent.Event
}

type agentDoneMsg struct {
	ch <-chan Agent.Event
}

// exitAfterStatusMsg lets the renderer paint the final status before Bubble
// Tea returns control to the normal terminal buffer.
type exitAfterStatusMsg struct{}

type sessionCreatedMsg struct {
	fileName string
	err      error
}

type sessionResumedMsg struct {
	fileName string
	messages []conversation.Message
	err      error
}

type sessionDeletedMsg struct {
	picker   *resumePicker
	fileName string
	err      error
}

type contextCompactedMsg struct {
	err error
}

// 缺少当前会话时先创建会话，再取得 ChatStream 事件通道。
func startAgent(ctx context.Context, application *App.App, input string) tea.Cmd {
	return func() tea.Msg {
		if application.CurrentSessionFileName() == "" {
			if _, err := application.NewSession(ctx); err != nil {
				return agentStartedMsg{err: err}
			}
		}
		return agentStartedMsg{ch: application.ChatStream(ctx, input)}
	}
}

// 每次只读取一个事件，关闭时返回完成消息；附带通道以区分新旧运行。
func waitForAgentEvent(ch <-chan Agent.Event) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return agentDoneMsg{ch: ch}
		}
		return agentEventMsg{event: event, ch: ch}
	}
}

// 等待 100 毫秒后发送退出消息；这是时间缓冲，不是绘制完成确认。
func exitAfterStatus() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return exitAfterStatusMsg{}
	})
}

// 调用 App 新建会话，将文件名和错误封装为消息。
func createSession(ctx context.Context, application *App.App) tea.Cmd {
	return func() tea.Msg {
		fileName, err := application.NewSession(ctx)
		return sessionCreatedMsg{fileName: fileName, err: err}
	}
}

// 先切换业务会话，成功后获取消息快照交给界面重建历史。
func resumeSession(ctx context.Context, application *App.App, fileName string) tea.Cmd {
	return func() tea.Msg {
		if err := application.SwitchSession(ctx, fileName); err != nil {
			return sessionResumedMsg{fileName: fileName, err: err}
		}
		return sessionResumedMsg{
			fileName: fileName,
			messages: application.CurrentMessages(),
		}
	}
}

// 把删除结果与发起操作的菜单实例一起返回，以便检查结果是否过期。
func deleteSession(ctx context.Context, application *App.App, picker *resumePicker, fileName string) tea.Cmd {
	return func() tea.Msg {
		return sessionDeletedMsg{
			picker:   picker,
			fileName: fileName,
			err:      application.DeleteSession(ctx, fileName),
		}
	}
}

// 调用 App 压缩模型上下文，界面中的 transcript 保持独立。
func compactContext(ctx context.Context, application *App.App) tea.Cmd {
	return func() tea.Msg {
		return contextCompactedMsg{err: application.CompactContext(ctx)}
	}
}
