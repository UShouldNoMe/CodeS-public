package tui

// 从已提交的显示记录中倒序寻找最后一条非空助手回复，复制其原始文本。
// 复制不包含界面前缀、显示裁剪或仍在生成的 stream；剪贴板写入在 Cmd 中执行。

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type replyCopiedMsg struct {
	err error
}

// 从后向前寻找助手条目，跳过用户、工具、系统和错误文本。
func lastAssistantText(entries []transcriptEntry) (string, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.kind == entryAssistant && strings.TrimSpace(entry.content) != "" {
			return entry.content, true
		}
	}
	return "", false
}

// Capture the text before starting the command so later session switches cannot
// change what gets copied. Run the system clipboard operation outside Update.
// 先固定复制文本，执行 Cmd 时再写剪贴板，避免期间切换会话改变目标。
func copyReply(content string, writeClipboard func(string) error) tea.Cmd {
	return func() tea.Msg {
		return replyCopiedMsg{err: writeClipboard(content)}
	}
}
