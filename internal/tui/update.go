package tui

// 界面状态机：统一接收消息，修改显示状态，决定下一项任务。
// 耗时操作通常放到 tea.Cmd 中执行，再把结果交回 Update，避免阻塞键盘处理。
// 菜单指针和 Agent 通道用于识别结果来源，防止旧任务覆盖新界面。

import (
	"CodeS/internal/Agent"
	"CodeS/internal/conversation"
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// 消息分发入口：按消息类型改变状态，返回下一项任务供框架执行。
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case modelsLoadedMsg:
		if m.modelPicker != msg.picker {
			return m, nil
		}
		msg.picker.loading = false
		msg.picker.cancel()
		if msg.err != nil {
			msg.picker.err = msg.err.Error()
		} else {
			msg.picker.models = msg.models
		}
		return m, nil
	case replyCopiedMsg:
		if msg.err != nil {
			m.status = "copy failed"
			m.addEntry(entryError, "copy: "+msg.err.Error())
		} else {
			m.status = "assistant reply copied to clipboard"
		}
		return m, nil

	case contextCompactedMsg:
		m.sessionBusy = false
		if msg.err != nil {
			m.status = "compact failed"
			m.addEntry(entryError, "compact: "+msg.err.Error())
		} else {
			m.status = "context compacted; full history preserved"
		}
		if m.quitAfterSession {
			return m, m.exitWithUsage()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampScroll()
		return m, nil

	case tea.MouseMsg:
		if m.resumePicker != nil || m.modelPicker != nil || msg.Y < 1 || msg.Y >= m.height-(inputBarHeight+1) {
			return m, nil
		}
		switch msg.Type {
		case tea.MouseWheelUp:
			m.scrollBody(-3)
		case tea.MouseWheelDown:
			m.scrollBody(3)
		}
		return m, nil

	case exitAfterStatusMsg:
		if !m.exitPending {
			return m, nil
		}
		m.exitPending = false
		m.quitting = true
		return m, tea.Quit

	case tea.KeyMsg:
		return m.handleKey(msg)

	case approvalRequestMsg:
		if err := msg.request.ctx.Err(); err != nil {
			msg.request.response <- false
			return m, waitForApproval(m.approvals)
		}
		m.pendingApproval = &msg.request
		m.status = "permission required"
		return m, nil

	case agentStartedMsg:
		if msg.err != nil {
			m.finishRun()
			m.addEntry(entryError, "start chat: "+msg.err.Error())
			return m, nil
		}
		m.agentCh = msg.ch
		m.status = "agent running"
		return m, waitForAgentEvent(msg.ch)

	case agentEventMsg:
		if msg.ch != m.agentCh {
			return m, nil
		}
		m.handleAgentEvent(msg.event)
		return m, waitForAgentEvent(msg.ch)

	case agentDoneMsg:
		if msg.ch == m.agentCh {
			m.commitStream()
			m.finishRun()
		}
		return m, nil

	case sessionCreatedMsg:
		m.sessionBusy = false
		if msg.err != nil {
			m.cancelSessionChange()
			m.addEntry(entryError, "new session: "+msg.err.Error())
			m.status = "ready"
			if m.quitAfterSession {
				return m, m.exitWithUsage()
			}
			return m, nil
		}
		m.transcript = nil
		m.stream = ""
		m.scrollTop = 0
		m.followBottom = true
		m.shownToolResults = make(map[string]bool)
		m.completeSessionChange("new session created")
		if m.quitAfterSession {
			return m, m.exitWithUsage()
		}
		return m, nil

	case sessionResumedMsg:
		m.sessionBusy = false
		if msg.err != nil {
			m.cancelSessionChange()
			if m.resumePicker != nil {
				m.resumePicker.err = msg.err.Error()
				m.status = "resume failed"
				if m.quitAfterSession {
					return m, m.exitWithUsage()
				}
				return m, nil
			}
			m.addEntry(entryError, fmt.Sprintf("resume session: %v", msg.err))
			m.status = "ready"
			if m.quitAfterSession {
				return m, m.exitWithUsage()
			}
			return m, nil
		}
		m.transcript = transcriptFromMessages(msg.messages)
		m.resumePicker = nil
		m.stream = ""
		m.scrollTop = 0
		m.followBottom = true
		m.shownToolResults = make(map[string]bool)
		m.completeSessionChange(fmt.Sprintf("session resumed (%d stored messages)", len(msg.messages)))
		if m.quitAfterSession {
			return m, m.exitWithUsage()
		}
		return m, nil

	case sessionDeletedMsg:
		m.sessionBusy = false
		if m.quitAfterSession {
			return m, m.exitWithUsage()
		}
		if m.resumePicker != msg.picker {
			return m, nil
		}
		if msg.err != nil {
			m.resumePicker.err = msg.err.Error()
			m.status = "delete failed"
			return m, nil
		}
		for i, info := range m.resumePicker.sessions {
			if info.FileName != msg.fileName {
				continue
			}
			m.resumePicker.sessions = append(
				m.resumePicker.sessions[:i],
				m.resumePicker.sessions[i+1:]...,
			)
			break
		}
		if m.resumePicker.selected >= len(m.resumePicker.sessions) {
			m.resumePicker.selected = max(0, len(m.resumePicker.sessions)-1)
		}
		m.status = "session deleted"
		return m, nil

	case resumePickerLoadedMsg:
		if m.resumePicker != msg.picker {
			return m, nil // A cancelled or replaced picker must not reopen.
		}
		m.resumePicker.loading = false
		if msg.err != nil {
			m.resumePicker.err = msg.err.Error()
			return m, nil
		}
		m.resumePicker.sessions = msg.sessions
		return m, nil
	}

	return m, nil
}

// 按忙碌状态、会话菜单、模型菜单、授权、普通输入的顺序分配按键。
// 运行中 Ctrl+C 取消 Agent，空闲时请求退出；编辑仅支持末尾追加和删除。
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessionBusy {
		if msg.String() == "ctrl+c" {
			// Let the in-flight session operation finish before App.Close runs.
			m.quitAfterSession = true
			m.status = "finishing session operation before exit"
		}
		return m, nil
	}
	if m.resumePicker != nil {
		return m.handleResumeKey(msg)
	}
	if m.modelPicker != nil {
		return m.handleModelKey(msg)
	}
	if m.pendingApproval != nil {
		switch msg.String() {
		case "y", "Y", "enter":
			return m.answerApproval(true)
		case "n", "N", "esc":
			return m.answerApproval(false)
		case "ctrl+c":
			if m.cancelRun != nil {
				m.cancelRun()
			}
			return m.answerApproval(false)
		}
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		if m.running {
			if m.cancelRun != nil {
				m.cancelRun()
			}
			m.status = "cancelling agent"
			return m, nil
		}
		return m, m.exitWithUsage()

	case "enter", "ctrl+j":
		if m.running {
			return m, nil
		}
		if suggestions := m.commandSuggestions(); len(suggestions) > 0 && m.suggestionHeight() > 0 {
			selected := suggestions[min(m.commandSelection, len(suggestions)-1)].name
			if m.input != selected {
				m.input = selected
				m.commandSelection = 0
				return m, nil
			}
		}
		line := strings.TrimSpace(m.input)
		if line == "" {
			return m, nil
		}
		m.input = ""
		m.commandDismissed = false
		m.commandSelection = 0
		if strings.HasPrefix(line, "/") {
			return m.handleCommand(line)
		}

		m.addEntry(entryUser, line)
		m.stream = ""
		m.running = true
		m.status = "starting agent"
		runCtx, cancel := context.WithCancel(m.ctx)
		m.cancelRun = cancel
		return m, startAgent(runCtx, m.app, line)

	case "backspace", "ctrl+h":
		if !m.running {
			m.input = trimLastRune(m.input)
			m.commandDismissed = false
			m.commandSelection = 0
		}
		return m, nil

	case "ctrl+u":
		if !m.running {
			m.input = ""
			m.commandDismissed = false
			m.commandSelection = 0
		}
		return m, nil

	case "up", "down":
		if suggestions := m.commandSuggestions(); len(suggestions) > 0 && m.suggestionHeight() > 0 {
			if msg.String() == "up" {
				m.commandSelection = max(0, m.commandSelection-1)
			} else {
				m.commandSelection = min(len(suggestions)-1, m.commandSelection+1)
			}
		}
		return m, nil

	case "esc":
		if len(m.commandSuggestions()) > 0 {
			m.commandDismissed = true
		}
		return m, nil
	}

	if !m.running && len(msg.Runes) > 0 {
		m.input += string(msg.Runes)
		m.commandDismissed = false
		m.commandSelection = 0
	}
	return m, nil
}

// 清除当前授权状态并回传用户选择，然后继续监听下一项请求。
func (m *Model) answerApproval(allowed bool) (tea.Model, tea.Cmd) {
	pending := m.pendingApproval
	m.pendingApproval = nil
	if pending != nil {
		pending.response <- allowed
	}
	if allowed {
		m.status = "permission granted"
	} else {
		m.status = "permission denied"
	}
	return m, waitForApproval(m.approvals)
}

// 将 Agent 事件转换为界面状态：增量文本暂存于 stream，阶段结束时提交。
// 工具结果可能经 ActionFinished 和 ObservationRecorded 到达，按调用 ID 去重。
func (m *Model) handleAgentEvent(event Agent.Event) {
	switch current := event.(type) {
	case Agent.RunStarted:
		m.status = "session running"

	case Agent.StepStarted:
		m.status = fmt.Sprintf("step %d: waiting for model", current.Step)

	case Agent.TextDelta:
		m.stream += current.Delta

	case Agent.ActionProposed:
		m.commitStream()
		m.status = fmt.Sprintf("step %d: %d tool call(s) proposed", current.Step, len(current.Calls))

	case Agent.ActionStarted:
		m.status = "running tool " + current.Call.Name

	case Agent.ActionFinished:
		m.addEntry(entryTool, formatToolResult(current.Record, &current.Call))
		m.shownToolResults[current.Record.ToolCallID] = true
		m.status = "tool " + current.Call.Name + " finished"
		if command := current.Record.Command; command != nil {
			m.status = fmt.Sprintf("tool %s finished: command %s (exit %d)", current.Call.Name, command.Status, command.ExitCode)
		}

	case Agent.ObservationRecorded:
		if !m.shownToolResults[current.Record.ToolCallID] {
			m.addEntry(entryTool, formatToolResult(current.Record, nil))
			m.shownToolResults[current.Record.ToolCallID] = true
		}

	case Agent.RunCompleted:
		m.commitStream()
		m.sessionUsage.InputTokens += current.Result.Usage.InputTokens
		m.sessionUsage.OutputTokens += current.Result.Usage.OutputTokens
		m.status = fmt.Sprintf(
			"completed in %d step(s), tokens %d in / %d out",
			current.Result.Steps,
			current.Result.Usage.InputTokens,
			current.Result.Usage.OutputTokens,
		)

	case Agent.RunStopped:
		m.commitStream()
		m.status = fmt.Sprintf("stopped at step %d: %s", current.Step, current.Reason)

	case Agent.RunFailed:
		m.commitStream()
		m.addEntry(entryError, fmt.Sprintf("%s: %v", current.Phase, current.Err))
		m.status = "agent failed"
	}
}

// 保存离开会话前的用量快照，等待切换成功后再展示和重置。
func (m *Model) beginSessionChange() {
	usage := m.sessionUsage
	m.leavingUsage = &usage
}

// 切换失败只撤销快照，保留原有用量。
func (m *Model) cancelSessionChange() {
	m.leavingUsage = nil
}

// 切换成功后显示上一会话的本次用量，并重置新进入会话的统计。
func (m *Model) completeSessionChange(nextStatus string) {
	if m.leavingUsage == nil {
		m.status = nextStatus
	} else {
		m.status = sessionUsageStatus(*m.leavingUsage) + " · " + nextStatus
		m.completedSessionUsage = append(m.completedSessionUsage, *m.leavingUsage)
	}
	m.leavingUsage = nil
	m.sessionUsage = conversation.Usage{}
}

// 先写入用量状态，再发起延时退出任务，为最后一帧留出显示时间。
func (m *Model) exitWithUsage() tea.Cmd {
	m.quitAfterSession = false
	m.exitPending = true
	m.status = sessionUsageStatus(m.sessionUsage) + " · exiting CodeS"
	return exitAfterStatus()
}

// 格式化本次进入会话以来累计的输入、输出及总 token 数。
func sessionUsageStatus(usage conversation.Usage) string {
	return fmt.Sprintf(
		"session tokens: %d input / %d output / %d total",
		usage.InputTokens,
		usage.OutputTokens,
		usage.InputTokens+usage.OutputTokens,
	)
}

// 恢复空闲状态并释放取消函数，正文提交由调用处另行处理。
func (m *Model) finishRun() {
	m.running = false
	m.agentCh = nil
	if m.cancelRun != nil {
		m.cancelRun()
		m.cancelRun = nil
	}
}

// 将暂存增量合并为一条助手显示记录；此操作不负责持久化。
func (m *Model) commitStream() {
	if m.stream == "" {
		return
	}
	m.addEntry(entryAssistant, m.stream)
	m.stream = ""
}

// 跳过纯空白内容，向本地显示记录追加条目。
func (m *Model) addEntry(kind entryKind, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	m.transcript = append(m.transcript, transcriptEntry{kind: kind, content: content})
}

// ReadFile 只显示目标路径与状态，不把读取内容写入聊天区。
func formatToolResult(result conversation.ToolResult, call *conversation.ToolCall) string {
	if result.ToolName == "ReadFile" {
		if call != nil {
			if path, ok := call.Arguments["path"].(string); ok && path != "" {
				return fmt.Sprintf("%s %q [%s]", result.ToolName, path, result.Status)
			}
		}
		return fmt.Sprintf("%s [%s]", result.ToolName, result.Status)
	}
	status := string(result.Status)
	if command := result.Command; command != nil {
		status = fmt.Sprintf("%s; command %s (exit %d)", status, command.Status, command.ExitCode)
	}
	return fmt.Sprintf("%s [%s]\n%s", result.ToolName, status, result.Content)
}
