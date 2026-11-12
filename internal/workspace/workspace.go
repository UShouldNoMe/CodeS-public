package workspace

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// DefaultResultLimit 是文件搜索类工具一次默认最多返回的结果数，既避免输出过长，
	// 也避免模型一次扫描过多文件。
	DefaultResultLimit = 200
	// MaxResultLimit 是模型主动传入 limit 时也不能超过的硬上限。
	MaxResultLimit = 2000
	// MaxToolOutputChars 是单个工具结果允许保留的最大文本长度。
	MaxToolOutputChars = 20_000
)

// ErrStopWalk filepath.WalkDir 的回调通过返回这个哨兵错误提前结束遍历。
// 调用方可以用 errors.Is 判断这是“达到结果上限”，而不是真正的 IO 错误。
var ErrStopWalk = errors.New("stop walking")

// Workspace 表示 Agent 可以通过文件工具访问的工作区。
//
// 它是一个“应用层路径沙箱”：ReadFile、WriteFile、EditFile、Glob、Grep
// 等工具在访问文件前调用 resolve，把模型传入的路径限制在 root 下面。
//
// 注意：Workspace 不是操作系统级沙箱。Bash 只把 root 当作工作目录，shell
// 命令仍然可以使用绝对路径访问工作区之外的资源；这需要独立的命令沙箱保护。
type Workspace struct {
	// root 始终保存绝对路径、清理后的路径，并且已经解析过根目录自身的符号链接。
	root       string
	protectGit bool
}

// NewWorkspace 创建并校验工作区。
//
// 初始化时把 root 规范化成真实绝对路径，后续所有路径判断都以同一个基准进行：
//
//  1. filepath.Abs 消除相对路径带来的歧义；
//  2. filepath.EvalSymlinks 解析根目录本身可能包含的符号链接；
//  3. os.Stat 确认根路径确实存在且是目录。
func NewWorkspace(root string) (Workspace, error) {
	if root == "" {
		root = "."
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	realRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return Workspace{}, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("workspace root %q is not a directory", root)
	}
	return Workspace{root: filepath.Clean(realRoot)}, nil
}

// WithProtectedGit prevents file tools from replacing worktree Git metadata.
func (w Workspace) WithProtectedGit() Workspace {
	w.protectGit = true
	return w
}

func (w Workspace) Root() string {
	return w.root
}

// Resolve 把模型提供的路径解析成工作区内的真实绝对路径。
//
// allowMissing 用来区分两类操作：
//
//   - 读取/编辑已有文件时为 false，目标必须已经存在；
//   - 创建新文件时为 true，目标可以不存在，但最近的已存在祖先目录必须安全。
//
// 返回前一定会调用 pathWithinRoot，因此 "../"、工作区外绝对路径和指向
// 工作区外的符号链接都会被拒绝。
func (w Workspace) Resolve(requestedPath string, allowMissing bool) (string, error) {
	if w.root == "" {
		return "", errors.New("workspace is not initialized")
	}
	if requestedPath == "" {
		return "", errors.New("path is empty")
	}

	// 相对路径统一以工作区根目录为起点，而不是依赖进程当前目录。
	candidate := requestedPath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(w.root, candidate)
	}
	// Abs 还会 Clean 路径，例如把 a/../b 归一化为 b。
	absolutePath, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", requestedPath, err)
	}

	// 对已存在的目标解析整条路径上的符号链接，防止工作区内的链接
	// 指向工作区之外，例如 workspace/link -> C:\Users\...。
	realPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		if !allowMissing || !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve path %q: %w", requestedPath, err)
		}
		// 新文件本身还不存在，不能直接 EvalSymlinks；改为解析最近的
		// 已存在祖先，再把尚未创建的路径后缀接回去。
		realPath, err = resolveMissingPath(absolutePath)
		if err != nil {
			return "", fmt.Errorf("resolve path %q: %w", requestedPath, err)
		}
	}

	if !pathWithinRoot(w.root, realPath) {
		return "", fmt.Errorf("path %q is outside workspace %q", requestedPath, w.root)
	}
	if w.protectGit {
		// Check both the requested and resolved path, including symlink aliases.
		for _, candidate := range []string{absolutePath, realPath} {
			relative, _ := filepath.Rel(w.root, candidate)
			for _, component := range strings.Split(relative, string(filepath.Separator)) {
				if strings.EqualFold(component, ".git") {
					return "", errors.New("Git metadata is protected in isolated workspaces")
				}
			}
		}
	}
	return filepath.Clean(realPath), nil
}

// resolveMissingPath 安全地解析一个尚不存在的目标路径。
//
// 例如目标是 workspace/link/new/file.txt，且 link 是符号链接：直接检查字符串
// 前缀会误以为它在 workspace 内。本函数会向上找到已存在的 link，解析它的真实
// 位置，再拼回 new/file.txt，最终交给 pathWithinRoot 判断是否越界。
func resolveMissingPath(absolutePath string) (string, error) {
	ancestor := filepath.Clean(absolutePath)
	// 从目标开始逐级向父目录回退，直到找到真实存在的祖先。
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("no existing ancestor for %q", absolutePath)
		}
		ancestor = parent
	}

	// Lstat 不跟随符号链接；找到祖先后再在这里解析它的真实位置。
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	// suffix 是尚未存在的那一段路径。把它接到真实祖先后面，就得到了
	// 新文件最终将要落到的真实位置。
	suffix, err := filepath.Rel(ancestor, absolutePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(realAncestor, suffix), nil
}

// pathWithinRoot 判断 candidate 是否等于 root，或者位于 root 的子目录中。
//
// 这里故意不用 strings.HasPrefix：如果根目录是 C:\code，字符串前缀判断会把
// C:\code-evil 也误认为子目录。filepath.Rel 得到的是路径语义上的相对关系，
// 只有结果为 ".." 或以 "..\" 开头时才说明 candidate 逃出了根目录。
func pathWithinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// RelativePath 把内部使用的绝对路径转换成适合返回给模型的工作区相对路径。
// ToSlash 让 Windows 和 Unix 都统一显示为 "dir/file.go"。
//
// 这个函数只负责展示，不承担安全检查；调用它之前路径应该已经通过 Resolve。
func (w Workspace) RelativePath(absolutePath string) string {
	relative, err := filepath.Rel(w.root, absolutePath)
	if err != nil {
		return filepath.ToSlash(absolutePath)
	}
	if relative == "." {
		return "."
	}
	return filepath.ToSlash(relative)
}

// JoinResolved 用于把已经验证过的目录和目录项名称拼接起来。
// 单独保留成方法，可以避免各文件工具重复接触 Workspace 的内部 root 字段。
func (w Workspace) JoinResolved(basePath, name string) string {
	return filepath.Join(basePath, name)
}

// ContextError 在开始耗时操作前快速检查调用是否已经被取消。
func ContextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// RequiredStringArgument 读取必填字符串参数，并按需禁止空字符串。
// 工具在真正接触文件系统前统一做参数类型校验，避免到处出现类型断言。
func RequiredStringArgument(arguments map[string]any, name string, allowEmpty bool) (string, error) {
	value, ok := arguments[name]
	if !ok {
		return "", fmt.Errorf("argument %q is required", name)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	if !allowEmpty && strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("argument %q must not be empty", name)
	}
	return text, nil
}

// OptionalStringArgument 读取可选字符串参数；参数缺失、为 nil 或为空时返回默认值。
func OptionalStringArgument(arguments map[string]any, name, defaultValue string) (string, error) {
	value, ok := arguments[name]
	if !ok || value == nil {
		return defaultValue, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	if text == "" {
		return defaultValue, nil
	}
	return text, nil
}

// OptionalIntArgument 读取带上下限的可选整数参数。
//
// JSON 数字通常会被解码成 float64，所以这里接受多种整数表示；float64 只有在
// 没有小数部分时才合法，防止把 1.5 静默截断为 1。
func OptionalIntArgument(
	arguments map[string]any,
	name string,
	defaultValue, minimum, maximum int,
) (int, error) {
	value, ok := arguments[name]
	if !ok || value == nil {
		return defaultValue, nil
	}

	var number int
	switch typed := value.(type) {
	case int:
		number = typed
	case int32:
		number = int(typed)
	case int64:
		number = int(typed)
	case float64:
		if math.Trunc(typed) != typed {
			return 0, fmt.Errorf("argument %q must be an integer", name)
		}
		number = int(typed)
	default:
		return 0, fmt.Errorf("argument %q must be an integer", name)
	}

	if number < minimum || number > maximum {
		return 0, fmt.Errorf(
			"argument %q must be between %d and %d",
			name,
			minimum,
			maximum,
		)
	}
	return number, nil
}

// MatchGlob 判断工作区相对路径是否匹配 glob 表达式。
//
// 标准库 path.Match 不理解 "**"，所以先把路径按 "/" 拆段，再由
// matchGlobParts 递归实现 "** 可以跨越任意层目录" 的语义。
// 使用 path.Match 而不是 filepath.Match，是因为传入前已经用 ToSlash
// 把不同操作系统的路径统一成了斜杠形式。
func MatchGlob(patternValue, relativePath string) (bool, error) {
	patternValue = strings.TrimPrefix(filepath.ToSlash(patternValue), "./")
	relativePath = strings.TrimPrefix(filepath.ToSlash(relativePath), "./")
	if patternValue == "" {
		return false, errors.New("glob pattern is empty")
	}

	patternParts := strings.Split(patternValue, "/")
	pathParts := strings.Split(relativePath, "/")
	return matchGlobParts(patternParts, pathParts)
}

// matchGlobParts 递归匹配已经按目录层级拆开的 glob 和路径。
func matchGlobParts(patternParts, pathParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(pathParts) == 0, nil
	}
	if patternParts[0] == "**" {
		// 分支一：** 匹配零层目录，直接比较剩余 pattern。
		matched, err := matchGlobParts(patternParts[1:], pathParts)
		if err != nil || matched {
			return matched, err
		}
		// 分支二：** 依次吞掉一层或多层路径，再尝试匹配剩余 pattern。
		for index := range pathParts {
			matched, err = matchGlobParts(patternParts[1:], pathParts[index+1:])
			if err != nil || matched {
				return matched, err
			}
		}
		return false, nil
	}
	if len(pathParts) == 0 {
		return false, nil
	}

	matched, err := path.Match(patternParts[0], pathParts[0])
	if err != nil || !matched {
		return false, err
	}
	return matchGlobParts(patternParts[1:], pathParts[1:])
}

// ShouldSkipDirectory 避免 Glob/Grep 遍历体积大、噪声多或包含内部状态的目录。
// 这只是搜索范围优化，不是权限边界；真正的边界仍由 Workspace.Resolve 保证。
func ShouldSkipDirectory(name string) bool {
	switch name {
	case ".git", ".idea", ".vscode", "node_modules", "vendor", ".venv", "venv":
		return true
	default:
		return false
	}
}

// TruncateText 对进入模型上下文的文本做最终长度保护，并告诉调用方是否发生截断。
func TruncateText(text string, maximum int) (string, bool) {
	if len(text) <= maximum {
		return text, false
	}
	return text[:maximum] + "\n... (truncated)", true
}
