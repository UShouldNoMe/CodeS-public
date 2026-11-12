package tools

import (
	"CodeS/internal/workspace"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

type Glob struct {
	workspace workspace.Workspace
}

func NewGlob(workspace workspace.Workspace) Glob {
	return Glob{workspace: workspace}
}

func (g Glob) Definition() ToolDef {
	return ToolDef{
		Name:        "Glob",
		Description: "在工作区内递归查找符合 glob 模式的文件，支持 **。用于按文件名或路径模式查找文件；查看某个目录的直接子项请使用 ListFile。结果路径相对于工作区根目录。",
		Type:        "object",
		Properties: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "文件匹配模式，例如 **/*.go。",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "工作区内开始查找的目录，默认是工作区根目录。",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "最多返回的文件数量，默认 200，最大 2000。",
				"minimum":     1,
				"maximum":     workspace.MaxResultLimit,
			},
		},
		Required:          []string{"pattern"},
		CouldBeParalleled: true,
		Authority:         CategoryRead,
	}
}

func (g Glob) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	patternValue, err := workspace.RequiredStringArgument(arguments, "pattern", false)
	if err != nil {
		return nil, err
	}
	basePath, err := workspace.OptionalStringArgument(arguments, "path", ".")
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
	if _, err := workspace.MatchGlob(patternValue, "validation"); err != nil {
		return nil, err
	}

	resolvedBase, err := g.workspace.Resolve(basePath, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedBase)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", basePath)
	}
	var matches []string
	err = filepath.WalkDir(resolvedBase, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := workspace.ContextError(ctx); err != nil {
			return err
		}
		if entry.IsDir() {
			if currentPath != resolvedBase && workspace.ShouldSkipDirectory(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		relativeToBase, err := filepath.Rel(resolvedBase, currentPath)
		if err != nil {
			return err
		}
		matched, err := workspace.MatchGlob(patternValue, relativeToBase)
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		matches = append(matches, g.workspace.RelativePath(currentPath))
		if len(matches) >= limit {
			return workspace.ErrStopWalk
		}
		return nil
	})
	if err != nil && !errors.Is(err, workspace.ErrStopWalk) {
		return nil, err
	}

	sort.Strings(matches)
	result := make([]any, len(matches))
	for index, match := range matches {
		result[index] = match
	}
	return result, nil
}
