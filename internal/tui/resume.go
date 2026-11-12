package tui

// 会话菜单负责列出、选择、恢复和删除历史会话，实际存储操作通过 App 完成。
// 列表排除当前会话，按更新时间优先排序；恢复成功后 Update 重建显示记录。
// 异步结果携带菜单指针，避免用户退出菜单后旧结果将它重新打开。

import (
	"CodeS/internal/persistent"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type resumePicker struct {
	sessions []persistent.SessionInfo
	selected int
	loading  bool
	err      string
}

type resumePickerLoadedMsg struct {
	picker   *resumePicker
	sessions []persistent.SessionInfo
	err      error
}

// 异步读列表，排除当前会话，按更新时间、创建时间和文件名依次降序排列。
func (m *Model) openResumePicker() (tea.Model, tea.Cmd) {
	picker := &resumePicker{loading: true}
	m.resumePicker = picker
	ctx, app := m.ctx, m.app
	currentFileName := app.CurrentSessionFileName()
	return m, func() tea.Msg {
		sessions, err := app.ListSessions(ctx)
		previous := make([]persistent.SessionInfo, 0, len(sessions))
		for _, info := range sessions {
			if info.FileName != currentFileName {
				previous = append(previous, info)
			}
		}
		sort.SliceStable(previous, func(i, j int) bool {
			if previous[i].UpdatedAt.Equal(previous[j].UpdatedAt) {
				if previous[i].CreatedAt.Equal(previous[j].CreatedAt) {
					return previous[i].FileName > previous[j].FileName
				}
				return previous[i].CreatedAt.After(previous[j].CreatedAt)
			}
			return previous[i].UpdatedAt.After(previous[j].UpdatedAt)
		})
		return resumePickerLoadedMsg{picker: picker, sessions: previous, err: err}
	}
}

// 恢复和删除期间设置 sessionBusy 防止重复操作。
// 此菜单的 Ctrl+C 直接退出，与主界面先显示用量的路径不同。
func (m *Model) handleResumeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.resumePicker
	switch msg.String() {
	case "esc":
		m.resumePicker = nil
		return m, nil
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up":
		if p.selected > 0 {
			p.selected--
			p.err = ""
		}
	case "down":
		if p.selected+1 < len(p.sessions) {
			p.selected++
			p.err = ""
		}
	case "d":
		if !p.loading && len(p.sessions) > 0 {
			m.sessionBusy = true
			p.err = ""
			fileName := p.sessions[p.selected].FileName
			m.status = "deleting session"
			return m, deleteSession(m.ctx, m.app, p, fileName)
		}
	case "enter", "ctrl+j":
		if !p.loading && len(p.sessions) > 0 {
			m.sessionBusy = true
			p.err = ""
			fileName := p.sessions[p.selected].FileName
			m.beginSessionChange()
			m.status = "resuming session"
			return m, resumeSession(m.ctx, m.app, fileName)
		}
	}
	return m, nil
}

// 以本地时区显示时间，零值显示 unknown。
func resumeTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.Local().Format("2006-01-02 15:04")
}

// A stored prompt can contain newlines, tabs or escape codes. Render it as one
// plain line so it cannot break the menu layout or send terminal controls.
// 清除 ANSI 转义和控制字符，合并空白，使内容可作为单行标题。
func resumeTitle(value string) string {
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "(empty session)"
	}
	return value
}

// 通常每项占标题和时间两行，小窗口改用精简提示；选中项保持可见。
func (m *Model) renderResumePicker() string {
	p := m.resumePicker
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	if width < 28 || height < 9 {
		lines := []string{"Resume session", "Enlarge terminal", "Esc back"}
		if height < len(lines) {
			lines = lines[:height]
		}
		for i := range lines {
			lines[i] = ansi.Truncate(lines[i], width, "")
		}
		return strings.Join(lines, "\n")
	}
	inner := min(width-4, 92)
	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	lines := []string{"Resume session", "↑/↓ select · Enter resume · d delete · Esc back", ""}
	switch {
	case p.loading:
		lines = append(lines, "Loading sessions…")
	case len(p.sessions) == 0:
		lines = append(lines, "No previous sessions found.")
	default:
		rows := max(1, (height-8)/2)
		start := max(0, p.selected-rows+1)
		end := min(len(p.sessions), start+rows)
		for i := start; i < end; i++ {
			info := p.sessions[i]
			prefix := "  "
			if i == p.selected {
				prefix = "› "
			}
			title := ansi.Truncate(prefix+resumeTitle(info.FirstPrompt), inner, "…")
			if i == p.selected {
				title = selectedStyle.Render(title)
			}
			metadata := fmt.Sprintf("  Updated %s · Created %s", resumeTime(info.UpdatedAt), resumeTime(info.CreatedAt))
			lines = append(lines, title, dimStyle.Render(ansi.Truncate(metadata, inner, "…")))
		}
		lines = append(lines, fmt.Sprintf("%d / %d", p.selected+1, len(p.sessions)))
	}
	if p.err != "" {
		lines = append(lines, "Error: "+resumeTitle(p.err))
	}
	if m.sessionBusy {
		lines = append(lines, m.status)
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], inner, "…")
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
		Width(inner + 2).Render(strings.Join(lines, "\n"))
}
