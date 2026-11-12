package tools

import (
	"CodeS/internal/workspace"
	"context"
	"fmt"
	"os"
)

type ListFile struct {
	workspace workspace.Workspace
}

type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Path  string `json:"path"`
}

func NewListFile(workspace workspace.Workspace) ListFile {
	return ListFile{workspace: workspace}
}

func (p ListFile) Definition() ToolDef {
	return ToolDef{
		Name: "ListFile",
		Description: "列出工作区内指定目录的直接子项，不递归进入子目录。\n" +
			"使用 path 指定目录，默认为工作区根目录。只能列出工作区内的目录，不能用此工具读取文件内容。\n" +
			"返回每个子项的名称、是否为目录、大小和工作区相对路径。需要读取文件内容时使用 ReadFile；需要按模式递归查找文件时使用 Glob。",
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "工作区内的目录路径，默认是当前工作区。",
			},
		},
		CouldBeParalleled: true,
		Authority:         CategoryRead,
	}
}

func (p ListFile) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	if err := workspace.ContextError(ctx); err != nil {
		return nil, err
	}
	requestedPath, err := workspace.OptionalStringArgument(arguments, "path", ".")
	if err != nil {
		return nil, err
	}
	resolvedPath, err := p.workspace.Resolve(requestedPath, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", requestedPath)
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		return nil, err
	}

	result := make([]any, 0, len(entries))
	for _, entry := range entries {
		if err := workspace.ContextError(ctx); err != nil {
			return nil, err
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return nil, err
		}
		entryPath := p.workspace.RelativePath(
			p.workspace.JoinResolved(resolvedPath, entry.Name()),
		)
		result = append(result, FileInfo{
			Name:  entry.Name(),
			IsDir: entry.IsDir(),
			Size:  entryInfo.Size(),
			Path:  entryPath,
		})
	}
	return result, nil
}
