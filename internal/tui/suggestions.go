package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type slashCommand struct {
	name        string
	description string
}

// Keep the completion list aligned with the commands handled in commands.go.
var slashCommands = []slashCommand{
	{"/compact", "summarize context"},
	{"/copy", "copy last assistant reply"},
	{"/exit", "exit CodeS"},
	{"/help", "show commands"},
	{"/mode", "show or change execution mode"},
	{"/model", "select provider and model"},
	{"/new", "start a new session"},
	{"/quit", "exit CodeS"},
	{"/resume", "select a previous session"},
}

func (m *Model) commandSuggestions() []slashCommand {
	if m.running || m.sessionBusy || m.pendingApproval != nil || m.commandDismissed ||
		!strings.HasPrefix(m.input, "/") || strings.ContainsAny(m.input, " \t\n") {
		return nil
	}
	var matches []slashCommand
	for _, command := range slashCommands {
		if strings.HasPrefix(command.name, m.input) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m *Model) suggestionHeight() int {
	// Keep at least one row available for the transcript.
	return min(len(m.commandSuggestions()), 7, max(0, m.height-(inputBarHeight+3)))
}

func (m *Model) renderCommandSuggestions() []string {
	matches := m.commandSuggestions()
	rows := m.suggestionHeight()
	if rows == 0 {
		return nil
	}
	selected := min(m.commandSelection, len(matches)-1)
	start := max(0, selected-rows+1)
	lines := make([]string, 0, rows)
	for i := start; i < min(len(matches), start+rows); i++ {
		prefix := "  "
		if i == selected {
			prefix = "› "
		}
		line := prefix + matches[i].name + "  " + matches[i].description
		if m.width > 0 {
			line = ansi.Truncate(line, m.width, "…")
		}
		lines = append(lines, line)
	}
	return lines
}
