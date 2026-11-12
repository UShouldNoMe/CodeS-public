package tools

import (
	"CodeS/internal/workspace"
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

const maxReadLineBytes = 1024 * 1024

type ReadFile struct {
	workspace workspace.Workspace
}

type ReadFileResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Offset    int    `json:"offset"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
}

func NewReadFile(workspace workspace.Workspace) ReadFile {
	return ReadFile{workspace: workspace}
}

func (r ReadFile) Definition() ToolDef {
	return ToolDef{
		Name: "ReadFile",
		Description: "按行读取工作区内的文本文件，返回带行号的内容。\n" +
			"使用path指定文件路径，优先使用相对于工作区根目录的路径。绝对路径必须位于当前工作区内。\n" +
			"默认从文件开头读取 200 行；可用 offset 和 limit 读取大文件的指定部分。offset 从 0 开始，limit 最大为 2000 行。\n" +
			"返回内容使用从 1 开始的行号；如果结果因行数或输出长度限制而不完整，truncated 会标记为 true。\n" +
			"仅用于读取文件，不能读取目录。使用 ListFile 查看目录直接子项，使用 Glob 按模式查找文件。\n" +
			"若任务需要确认文件当前内容或编辑结果，可以再次读取文件。",
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "工作区内的文件路径。",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "从第几行开始读取，0 表示第一行。",
				"minimum":     0,
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "最多读取的行数，默认 200，最大 2000。",
				"minimum":     1,
				"maximum":     workspace.MaxResultLimit,
			},
		},
		Required:          []string{"path"},
		CouldBeParalleled: true,
		Authority:         CategoryRead,
	}
}

func (r ReadFile) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	if err := workspace.ContextError(ctx); err != nil {
		return nil, err
	}
	requestedPath, err := workspace.RequiredStringArgument(arguments, "path", false)
	if err != nil {
		return nil, err
	}
	offset, err := workspace.OptionalIntArgument(arguments, "offset", 0, 0, 10_000_000)
	if err != nil {
		return nil, err
	}
	limit, err := workspace.OptionalIntArgument(
		arguments,
		"limit",
		workspace.DefaultResultLimit,
		1,
		workspace.MaxResultLimit,
	)
	if err != nil {
		return nil, err
	}

	resolvedPath, err := r.workspace.Resolve(requestedPath, false)
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

	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxReadLineBytes)
	var content strings.Builder
	lineNumber := 0
	linesRead := 0
	truncated := false

	for scanner.Scan() {
		if err := workspace.ContextError(ctx); err != nil {
			return nil, err
		}
		lineNumber++
		if lineNumber <= offset {
			continue
		}
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return nil, fmt.Errorf("%q appears to be a binary file", requestedPath)
		}
		if linesRead >= limit {
			truncated = true
			break
		}

		formatted := fmt.Sprintf("%d\t%s\n", lineNumber, line)
		if content.Len()+len(formatted) > workspace.MaxToolOutputChars {
			truncated = true
			break
		}
		content.WriteString(formatted)
		linesRead++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %q: %w", requestedPath, err)
	}

	return []any{ReadFileResult{
		Path:      r.workspace.RelativePath(resolvedPath),
		Content:   strings.TrimSuffix(content.String(), "\n"),
		Offset:    offset,
		Lines:     linesRead,
		Truncated: truncated,
	}}, nil
}
