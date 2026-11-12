package tools

import (
	"CodeS/internal/workspace"
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
)

const maxEditableFileBytes = 10 * 1024 * 1024

type EditFile struct {
	workspace workspace.Workspace
}

type EditFileResult struct {
	Path     string `json:"path"`
	Replaced int    `json:"replaced"`
}

func NewEditFile(workspace workspace.Workspace) EditFile {
	return EditFile{workspace: workspace}
}

func (e EditFile) Definition() ToolDef {
	return ToolDef{
		Name:        "EditFile",
		Description: "在工作区内的文本文件中，将唯一出现的 old_string 精确替换为 new_string。old_string 必须在文件中恰好出现一次；未找到或出现多次时会报错。适合修改已有文件的一小段内容；完整创建或重写文件请使用 WriteFile。path 使用工作区内路径。",
		Type:        "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "工作区内的目标文件路径。",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "当前文件内容中必须恰好出现一次的原始文本；编辑前应已读取或已掌握该内容。",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "替换后的文本，可以为空字符串。",
			},
		},
		Required:  []string{"path", "old_string", "new_string"},
		Authority: CategoryWrite,
	}
}

func (e EditFile) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	if err := workspace.ContextError(ctx); err != nil {
		return nil, err
	}
	requestedPath, err := workspace.RequiredStringArgument(arguments, "path", false)
	if err != nil {
		return nil, err
	}
	oldString, err := workspace.RequiredStringArgument(arguments, "old_string", false)
	if err != nil {
		return nil, err
	}
	newString, err := workspace.RequiredStringArgument(arguments, "new_string", true)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := e.workspace.Resolve(requestedPath, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", requestedPath)
	}
	if info.Size() > maxEditableFileBytes {
		return nil, fmt.Errorf(
			"%q is too large to edit (%d bytes, maximum %d)",
			requestedPath,
			info.Size(),
			maxEditableFileBytes,
		)
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("%q appears to be a binary file", requestedPath)
	}
	original := string(content)
	count := strings.Count(original, oldString)
	if count == 0 {
		return nil, fmt.Errorf("old_string was not found in %q", requestedPath)
	}
	if count > 1 {
		return nil, fmt.Errorf(
			"old_string appears %d times in %q; it must be unique",
			count,
			requestedPath,
		)
	}

	updated := strings.Replace(original, oldString, newString, 1)
	if err := workspace.ContextError(ctx); err != nil {
		return nil, err
	}
	if err := os.WriteFile(resolvedPath, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, err
	}
	return []any{EditFileResult{
		Path:     e.workspace.RelativePath(resolvedPath),
		Replaced: 1,
	}}, nil
}
