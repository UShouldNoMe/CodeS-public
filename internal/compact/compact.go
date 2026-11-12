package compact

import (
	"CodeS/internal/conversation"
	"encoding/json"
	"fmt"
	"strings"
)

func FormatMessage(conv *conversation.Conversation) string {
	if conv == nil {
		return ""
	}
	builder := strings.Builder{}
	for _, message := range conv.Messages {
		switch message.Role {
		case conversation.RoleUser:
			{
				builder.WriteString("[USER]\n")
				builder.WriteString(message.Content)
				builder.WriteString("\n\n")
			}

		case conversation.RoleAssistant:
			{
				fmt.Fprintf(&builder, "[ASSISTANT]\n%s\n",
					message.Content,
				)
				for _, call := range message.ToolCalls {
					arg, _ := json.Marshal(call.Arguments)
					fmt.Fprintf(&builder,
						"[ASSISTANT_TOOL_CALL: %s]\n%s\n",
						call.Name,
						arg,
					)

				}
				builder.WriteString("\n")
			}

		case conversation.RoleTool:
			{
				if message.ToolResult != nil {
					fmt.Fprintf(
						&builder,
						"[TOOL_RESULT: %s]\n%s\n\n",
						message.ToolResult.ToolName,
						message.ToolResult.Content,
					)
				}
			}
		default:
			{
				fmt.Fprintf(&builder, "[%s]\n%s\n\n", message.Role, message.Content)
			}
		}
	}
	return strings.TrimSpace(builder.String())
}
