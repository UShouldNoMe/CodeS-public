package prompt

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptSeparatesSections(t *testing.T) {
	prompt := BuildSystemPrompt()

	for _, boundary := range []string{
		RoleSet + "\n\n" + BehaviorGuide,
		BehaviorGuide + "\n\n" + ToolUseInstructions,
		SecurityBoundaries + "\n\n" + TaskPatterns,
		TaskPatterns + "\n\n" + OutputFormat,
	} {
		if !strings.Contains(prompt, boundary) {
			t.Fatalf("system prompt does not contain section boundary %q", boundary)
		}
	}
}
