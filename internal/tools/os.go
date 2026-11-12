package tools

import (
	"context"
	"runtime"
)

type CurrentOS struct {
}

func (c CurrentOS) Definition() ToolDef {
	return ToolDef{
		Name:        "OS",
		Description: "获取 CodeS 当前进程运行的平台类型，例如 windows、linux 或 darwin。需要判断命令环境时使用此工具；不要根据用户电脑或目标项目的平台推断 CodeS 当前运行平台。",
		Type:        "object",
		Properties:  map[string]any{},
		Authority:   CategoryRead,
	}
}

func (c CurrentOS) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	ret := runtime.GOOS
	return []any{ret}, nil
}
