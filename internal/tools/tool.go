package tools

import (
	"context"
)

type Category string

const (
	CategoryRead    Category = "read"
	CategoryWrite   Category = "write"
	CategoryCommand Category = "command"
)

type ToolDef struct {
	Name              string
	Description       string
	Type              string // 通常来讲就是object
	Properties        map[string]any
	Required          []string
	CouldBeParalleled bool
	Authority         Category
}

type Tool interface {
	Definition() ToolDef
	Execute(ctx context.Context, arguments map[string]any) ([]any, error)
}
