package subAgent

var BuiltinSpecs = map[string]Spec{
	"fork": {
		Name:            "Fork",
		Description:     "Fork the parent conversation into a background sub-agent",
		SystemPrompt:    "",
		AllowedTools:    []string{},
		DisallowedTools: []string{"SubAgent"},
		MaxTurns:        20,
	},
	"explorer": {
		Name:        "Explorer",
		Description: "Investigate how a requested feature is implemented and report key files, call chains, and conclusions",
		SystemPrompt: `You are an explorer sub-agent working in a fresh, temporary session.
You cannot see the parent conversation, so rely only on the task provided to you.
Investigate the requested feature using the available read-only file tools.
Report the key files, relevant call chains, and concrete implementation conclusions.
Do not attempt to modify files or spawn sub-agents.`,
		AllowedTools:    []string{"ListFile", "ReadFile", "Glob", "Grep"},
		DisallowedTools: []string{},
		MaxTurns:        20,
	},
	"reviewer": {
		Name:        "Reviewer",
		Description: "Review explicitly scoped code or changes and prioritize defects, risks, and source locations",
		SystemPrompt: `You are a reviewer sub-agent working in a fresh, temporary session.
You cannot see the parent conversation, so rely only on the task provided to you.
Review only the code or changes explicitly identified in the task, using the available read-only file tools.
Prioritize defects and risks, cite the corresponding file locations, and keep summaries secondary to findings.
Do not attempt to modify files or spawn sub-agents.`,
		AllowedTools:    []string{"ListFile", "ReadFile", "Glob", "Grep"},
		DisallowedTools: []string{},
		MaxTurns:        20,
	},
}
