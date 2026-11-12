package tui

// 斜杠命令入口：handleKey 在回车提交时，将以 / 开头的输入转交这里。
// 各分支校验参数，再打开菜单、追加本地提示或返回异步任务；普通聊天走另一条路径。

import (
	"CodeS/internal/clipboard"
	"CodeS/internal/tools/manager"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// 将命令映射为本地提示、菜单或后台任务；参数不合法时追加错误条目。
func (m *Model) handleCommand(line string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return m, nil
	}

	switch parts[0] {
	case "/mode":
		if len(parts) == 1 {
			m.addEntry(entrySystem, "Execution mode: "+string(m.app.ExecutionMode()))
			return m, nil
		}
		if len(parts) != 2 {
			m.addEntry(entryError, "usage: /mode [manual|full_access]")
			return m, nil
		}
		if m.running || m.sessionBusy || m.pendingApproval != nil {
			m.addEntry(entryError, "execution mode can only be changed while idle")
			return m, nil
		}
		if err := m.app.SetExecutionMode(manager.ExecutionMode(parts[1])); err != nil {
			m.addEntry(entryError, err.Error())
			return m, nil
		}
		m.addEntry(entrySystem, "Execution mode: "+string(m.app.ExecutionMode()))
		return m, nil
	case "/model":
		if len(parts) != 1 {
			m.addEntry(entryError, "usage: /model")
			return m, nil
		}
		return m.openModelPicker()
	case "/copy":
		if len(parts) != 1 {
			m.addEntry(entryError, "usage: /copy")
			return m, nil
		}
		content, ok := lastAssistantText(m.transcript)
		if !ok {
			m.status = "no assistant reply to copy"
			return m, nil
		}
		m.status = "copying assistant reply"
		return m, copyReply(content, clipboard.Copy)

	case "/new":
		if len(parts) != 1 {
			m.addEntry(entryError, "usage: /new")
			return m, nil
		}
		m.beginSessionChange()
		m.status = "creating session"
		m.sessionBusy = true
		return m, createSession(m.ctx, m.app)

	case "/compact":
		if len(parts) != 1 {
			m.addEntry(entryError, "usage: /compact")
			return m, nil
		}
		m.status = "compacting context"
		m.sessionBusy = true
		return m, compactContext(m.ctx, m.app)

	case "/resume":
		if len(parts) != 1 {
			m.addEntry(entryError, "usage: /resume")
			return m, nil
		}
		return m.openResumePicker()

	case "/exit", "/quit":
		return m, m.exitWithUsage()

	case "/help":
		m.addEntry(entrySystem, strings.Join([]string{
			"Commands:",
			"/mode [manual|full_access] - show or change execution mode",
			"/model   - select provider and model",
			"/compact - summarize context while preserving visible history",
			"/resume  - select a previous session (Up/Down, Enter, Esc)",
			"/new     - start a new session",
			"/copy    - copy the last assistant reply to clipboard",
			"/exit",
		}, "\n"))
		return m, nil

	}

	m.addEntry(entryError, "unknown command: "+line)
	return m, nil
}
