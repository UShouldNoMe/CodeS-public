package tui

// 布局层：生成带 ANSI 样式的字符串，主界面分为标题、正文、输入条和状态栏。
// 菜单使用独立布局；正文裁剪只影响显示，不删除内存中的聊天记录。
// View 还会更新输入法光标配置，因此它并非完全没有副作用的字符串计算函数。

import (
	"CodeS/internal/conversation"
	"CodeS/internal/tools/manager"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var inputBarStyle = lipgloss.NewStyle().Background(lipgloss.Color("236"))

const inputBarHeight = 3

// 按退出、会话菜单、模型菜单、授权、普通聊天的顺序选取画面。
// 返回字符串交给框架输出，同时设置当前是否需要输入法光标。
func (m *Model) View() string {
	if m.quitting {
		m.setInputCursor(false, "")
		return ""
	}
	if m.resumePicker != nil {
		m.setInputCursor(false, "")
		return m.renderResumePicker()
	}
	if m.modelPicker != nil {
		m.setInputCursor(false, "")
		return m.renderModelPicker()
	}

	header := "CodeS"
	if m.app != nil {
		label := "Manual"
		if m.app.ExecutionMode() == manager.ModeFullAccess {
			label = "Full Access"
		}
		header += " · " + label
		if model := m.app.CurrentModel(); model != nil {
			header += " · " + resumeTitle(model.Provider+"/"+model.ID)
		}
	}

	if m.pendingApproval != nil {
		m.setInputCursor(false, "")
		return m.renderFrame(
			header,
			m.renderBody(),
			"Allow? [y/Enter] yes  [n/Esc] no",
		)
	}

	prompt := "> " + m.input
	if m.sessionBusy {
		prompt = "(working… Ctrl+C waits before exit)"
	}
	if m.running {
		prompt = "(agent is running; Ctrl+C cancels)"
	}
	m.setInputCursor(!m.sessionBusy && !m.running, prompt)
	return m.renderFrame(header, m.renderBody(), prompt)
}

func (m *Model) renderBody() string {
	body := m.renderTranscript()
	if m.pendingApproval == nil {
		return body
	}
	request := m.pendingApproval.request
	approval := strings.Join([]string{
		"Permission required",
		fmt.Sprintf("Tool: %s (%s)", request.ToolName, request.Authority),
		fmt.Sprintf("Arguments: %#v", request.Arguments),
	}, "\n")
	return joinViewSections(body, approval)
}

// renderInputBar gives the editable row a quiet three-line background. The
// prompt sits on its middle line, with one blank row above and below.
// 拼接上方空白、提示文字和下方空白，设计上共三行。
// 长提示经样式折行后可能超过三行，固定高度假设不一定成立。
func (m *Model) renderInputBar(prompt string) string {
	style := inputBarStyle
	if m.width > 0 {
		style = style.Width(m.width)
	}
	blank := style.Render("")
	return strings.Join([]string{blank, style.Render(prompt), blank}, "\n")
}

// setInputCursor records the physical cursor position that Windows IMEs use
// for their composing text. The prompt is two rows above the status row: the
// input bar's bottom padding occupies the remaining row.
// 使用终端显示宽度而非字节数计算一基列号，固定从状态行上移两行。
// 目前没有按长输入折行重新计算位置，也没有将列号限制在窗口宽度内。
func (m *Model) setInputCursor(enabled bool, prompt string) {
	if m.inputCursor == nil {
		return
	}
	if !enabled || m.height < 4 {
		m.inputCursor.set(false, 0, 0)
		return
	}
	m.inputCursor.set(true, ansi.StringWidth(prompt)+1, 2)
}

// renderFrame reserves the last row for status, so transient feedback remains
// visible even when the transcript is short. Bubble Tea renders this full frame
// into the alternate screen buffer on every update.
// 为标题留一行、输入条三行、状态栏一行，正文预算为 height-5。
// 正文不足时补空行，过长时按滚动位置截取；高度不足五行时退化为简单拼接。
func (m *Model) renderFrame(header, body, prompt string) string {
	if m.height < inputBarHeight+2 {
		return strings.Join([]string{header, body, prompt, m.status}, "\n")
	}

	inputBar := m.renderInputBar(prompt)
	suggestions := m.renderCommandSuggestions()
	body = m.visibleBody(body)
	lines := []string{header}
	if body != "" {
		lines = append(lines, strings.Split(body, "\n")...)
	}
	for len(lines) < m.height-(inputBarHeight+1)-len(suggestions) {
		lines = append(lines, "")
	}
	lines = append(lines, suggestions...)
	lines = append(lines, strings.Split(inputBar, "\n")...)
	lines = append(lines, m.status)
	return strings.Join(lines, "\n")
}

// 用空行连接两个非空区块，供正文与授权提示组合使用。
func joinViewSections(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "\n\n" + second
}

// 为各条记录添加角色前缀，再追加尚未提交的流式内容。
func (m *Model) renderTranscript() string {
	var builder strings.Builder
	for _, entry := range m.transcript {
		appendPrefixed(&builder, entryPrefix(entry.kind), entry.content)
		builder.WriteString("\n\n")
	}
	if m.stream != "" {
		appendPrefixed(&builder, "Assistant: ", m.stream)
	}
	return strings.TrimRight(builder.String(), "\n")
}

// 将持久化业务消息转换成界面条目，分别显示工具请求与工具结果。
func transcriptFromMessages(messages []conversation.Message) []transcriptEntry {
	entries := make([]transcriptEntry, 0, len(messages))
	readCalls := make(map[string]conversation.ToolCall)
	for _, message := range messages {
		switch message.Role {
		case conversation.RoleUser:
			entries = appendContent(entries, entryUser, message.Content)

		case conversation.RoleAssistant:
			entries = appendContent(entries, entryAssistant, message.Content)
			for _, call := range message.ToolCalls {
				if call.Name == "ReadFile" {
					readCalls[call.ID] = call
					continue
				}
				entries = append(entries, transcriptEntry{
					kind:    entryTool,
					content: fmt.Sprintf("requested %s %#v", call.Name, call.Arguments),
				})
			}

		case conversation.RoleTool:
			if message.ToolResult != nil {
				call := readCalls[message.ToolResult.ToolCallID]
				entries = append(entries, transcriptEntry{
					kind:    entryTool,
					content: formatToolResult(*message.ToolResult, &call),
				})
			}

		case conversation.RoleSystem:
			entries = appendContent(entries, entrySystem, message.Content)
		}
	}
	return entries
}

// 历史消息的纯空白正文不生成显示条目。
func appendContent(entries []transcriptEntry, kind entryKind, content string) []transcriptEntry {
	if strings.TrimSpace(content) == "" {
		return entries
	}
	return append(entries, transcriptEntry{kind: kind, content: content})
}

// 将条目类型映射为用户可见的角色名称。
func entryPrefix(kind entryKind) string {
	switch kind {
	case entryUser:
		return "You: "
	case entryAssistant:
		return "Assistant: "
	case entryTool:
		return "Tool: "
	case entryError:
		return "Error: "
	default:
		return "System: "
	}
}

// 首行显示角色前缀，后续行用空格缩进到正文起点。
func appendPrefixed(builder *strings.Builder, prefix, content string) {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		if index == 0 {
			builder.WriteString(prefix)
		} else {
			builder.WriteString(strings.Repeat(" ", utf8.RuneCountInString(prefix)))
		}
		builder.WriteString(line)
		if index < len(lines)-1 {
			builder.WriteByte('\n')
		}
	}
}

// 按终端宽度折行，使滚动单位与屏幕上看到的行一致。
func (m *Model) bodyLines(body string) []string {
	if body == "" {
		return nil
	}
	if m.width > 0 {
		body = ansi.Hardwrap(body, m.width, true)
	}
	return strings.Split(body, "\n")
}

func (m *Model) bodyHeight() int {
	return max(0, m.height-(inputBarHeight+2)-m.suggestionHeight())
}

func (m *Model) maxScrollTop() int {
	return max(0, len(m.bodyLines(m.renderBody()))-m.bodyHeight())
}

// 上滚后固定可见历史的首行；只有滚回底部才继续跟随新内容。
func (m *Model) scrollBody(delta int) {
	if m.bodyHeight() == 0 {
		return
	}
	bottom := m.maxScrollTop()
	start := m.scrollTop
	if m.followBottom {
		start = bottom
	}
	m.scrollTop = max(0, min(bottom, start+delta))
	m.followBottom = m.scrollTop == bottom
}

func (m *Model) clampScroll() {
	m.scrollTop = max(0, min(m.scrollTop, m.maxScrollTop()))
}

func (m *Model) visibleBody(body string) string {
	lines := m.bodyLines(body)
	count := m.bodyHeight()
	if count == 0 || len(lines) == 0 {
		return ""
	}
	bottom := max(0, len(lines)-count)
	start := m.scrollTop
	if m.followBottom {
		start = bottom
	}
	start = max(0, min(start, bottom))
	return strings.Join(lines[start:min(len(lines), start+count)], "\n")
}

// 按 UTF-8 码点删除末尾字符，避免截断中文字节；不处理组合字符簇。
func trimLastRune(value string) string {
	_, size := utf8.DecodeLastRuneInString(value)
	if size == 0 {
		return value
	}
	return value[:len(value)-size]
}
