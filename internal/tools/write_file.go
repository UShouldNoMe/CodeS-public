package tools

import (
	workspace2 "CodeS/internal/workspace"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type WriteFile struct {
	workspace workspace2.Workspace
}

type WriteFileResult struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Created bool   `json:"created"`
}

func NewWriteFile(workspace workspace2.Workspace) WriteFile {
	return WriteFile{workspace: workspace}
}

func (w WriteFile) Definition() ToolDef {
	return ToolDef{
		Name: "WriteFile",
		Description: "在工作区内写入完整文本内容；目标文件不存在时会创建所需的父目录，已存在时会被完整覆盖。\n" +
			"使用 path 指定工作区内的目标路径，使用 content 提供完整文件内容。" +
			"创建新文件时使用 WriteFile。修改已有文件时，优先使用 EditFile 做唯一文本片段替换；如果需要完整重写，先用 ReadFile 读取现有内容，再使用 WriteFile。\n" +
			"除非用户明确要求，否则不要新建 Markdown 文档或 README 文件。",
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "工作区内的目标文件路径。",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "要写入文件的完整文本内容。",
			},
		},
		Required:  []string{"path", "content"},
		Authority: CategoryWrite,
	}
}

func (w WriteFile) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	if err := workspace2.ContextError(ctx); err != nil {
		return nil, err
	}
	requestedPath, err := workspace2.RequiredStringArgument(arguments, "path", false)
	if err != nil {
		return nil, err
	}
	content, err := workspace2.RequiredStringArgument(arguments, "content", true)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := w.workspace.Resolve(requestedPath, true)
	if err != nil {
		return nil, err
	}

	created := true
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(resolvedPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", requestedPath)
		}
		created = false
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return nil, err
	}
	if err := workspace2.ContextError(ctx); err != nil {
		return nil, err
	}
	if err := os.WriteFile(resolvedPath, []byte(content), mode); err != nil {
		return nil, err
	}

	return []any{WriteFileResult{
		Path:    w.workspace.RelativePath(resolvedPath),
		Bytes:   len([]byte(content)),
		Created: created,
	}}, nil
}
