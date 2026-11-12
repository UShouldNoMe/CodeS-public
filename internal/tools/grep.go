package tools

import (
	"CodeS/internal/workspace"
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxSearchFileBytes = 2 * 1024 * 1024
	maxGrepLineChars   = 500
)

type Grep struct {
	workspace workspace.Workspace
}

type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

func NewGrep(workspace workspace.Workspace) Grep {
	return Grep{workspace: workspace}
}

func (g Grep) Definition() ToolDef {
	return ToolDef{
		Name:        "Grep",
		Description: "使用 Go RE2 正则表达式搜索工作区内的文本文件。path 可以是文件或目录；path 为目录时递归搜索。可用 include 限定文件路径模式。返回匹配文件的工作区相对路径、1 起始行号和匹配文本。查找文件名请使用 Glob；读取匹配位置的上下文请使用 ReadFile。",
		Type:        "object",
		Properties: map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Go RE2 语法的正则表达式。",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "工作区内的搜索起点，可以是文件或目录，默认是工作区根目录。",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "可选的文件路径 glob，例如 *.go 或 **/*_test.go。",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "最多返回的匹配数量，默认 200，最大 2000。",
				"minimum":     1,
				"maximum":     workspace.MaxResultLimit,
			},
		},
		Required:          []string{"pattern"},
		CouldBeParalleled: true,
		Authority:         CategoryRead,
	}
}

func (g Grep) Execute(ctx context.Context, arguments map[string]any) ([]any, error) {
	patternValue, err := workspace.RequiredStringArgument(arguments, "pattern", false)
	if err != nil {
		return nil, err
	}
	expression, err := regexp.Compile(patternValue)
	if err != nil {
		return nil, err
	}
	requestedPath, err := workspace.OptionalStringArgument(arguments, "path", ".")
	if err != nil {
		return nil, err
	}
	include, err := workspace.OptionalStringArgument(arguments, "include", "")
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
	if include != "" {
		if _, err := workspace.MatchGlob(include, "validation"); err != nil {
			return nil, err
		}
	}

	resolvedPath, err := g.workspace.Resolve(requestedPath, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, err
	}

	var matches []GrepMatch
	searchFile := func(filePath, relativeToBase string) error {
		if include != "" {
			matched, err := workspace.MatchGlob(include, relativeToBase)
			if err != nil {
				return err
			}
			baseMatched, baseErr := workspace.MatchGlob(include, filepath.Base(relativeToBase))
			if baseErr != nil {
				return baseErr
			}
			if !matched && !baseMatched {
				return nil
			}
		}
		return g.searchFile(ctx, filePath, expression, limit, &matches)
	}

	if info.Mode().IsRegular() {
		if err := searchFile(resolvedPath, filepath.Base(resolvedPath)); err != nil {
			return nil, err
		}
	} else if info.IsDir() {
		err = filepath.WalkDir(resolvedPath, func(currentPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := workspace.ContextError(ctx); err != nil {
				return err
			}
			if entry.IsDir() {
				if currentPath != resolvedPath && workspace.ShouldSkipDirectory(entry.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			entryInfo, err := entry.Info()
			if err != nil {
				return err
			}
			if !entryInfo.Mode().IsRegular() || entryInfo.Size() > maxSearchFileBytes {
				return nil
			}
			relativeToBase, err := filepath.Rel(resolvedPath, currentPath)
			if err != nil {
				return err
			}
			if err := searchFile(currentPath, relativeToBase); err != nil {
				return err
			}
			if len(matches) >= limit {
				return workspace.ErrStopWalk
			}
			return nil
		})
		if err != nil && !errors.Is(err, workspace.ErrStopWalk) {
			return nil, err
		}
	} else {
		return nil, errors.New("grep path is neither a regular file nor a directory")
	}

	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].Path == matches[right].Path {
			return matches[left].Line < matches[right].Line
		}
		return matches[left].Path < matches[right].Path
	})
	result := make([]any, len(matches))
	for index, match := range matches {
		result[index] = match
	}
	return result, nil
}

func (g Grep) searchFile(
	ctx context.Context,
	filePath string,
	expression *regexp.Regexp,
	limit int,
	matches *[]GrepMatch,
) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxReadLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		if err := workspace.ContextError(ctx); err != nil {
			return err
		}
		lineNumber++
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return nil
		}
		if !expression.MatchString(line) {
			continue
		}
		line, _ = workspace.TruncateText(line, maxGrepLineChars)
		*matches = append(*matches, GrepMatch{
			Path: g.workspace.RelativePath(filePath),
			Line: lineNumber,
			Text: line,
		})
		if len(*matches) >= limit {
			return nil
		}
	}
	return scanner.Err()
}
