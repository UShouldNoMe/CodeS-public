package llm

import (
	"CodeS/internal/conversation"
	"CodeS/internal/tools"
	"context"
)

type ModelInfo struct {
	Provider    string
	ID          string
	DisplayName string
}

type Request struct {
	Conversation *conversation.Conversation
	Tools        []tools.ToolDef
	Prompt       string
}

type Client interface {
	Generate(ctx context.Context, request *Request) (*conversation.Response, error)
	Stream(ctx context.Context, request *Request, onEvent func(conversation.StreamEvent) error) (*conversation.Response, error)
}

type ModelCatalog interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}
