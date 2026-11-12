package MCP

import (
	"CodeS/internal/tools"
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type RemoteTool struct {
	Def        tools.ToolDef
	RemoteName string
	Owner      remoteToolCaller
}

type remoteToolCaller interface {
	CallTool(ctx context.Context, args *mcp.CallToolParams) ([]any, error)
}

func (r RemoteTool) Definition() tools.ToolDef {
	return r.Def
}

func (r RemoteTool) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	params := new(mcp.CallToolParams{
		Name:      r.RemoteName,
		Arguments: arguments,
	})
	ret, err := r.Owner.CallTool(ctx, params)
	return ret, err
}

func makePublicName(serverName, remoteName string) string {
	return "mcp--" + serverName + "--" + remoteName
}
