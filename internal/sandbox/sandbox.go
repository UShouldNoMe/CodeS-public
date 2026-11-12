// Package sandbox provides the operating-system-specific command wrappers used
// to demonstrate how an Agent can execute shell commands inside an OS sandbox.
//
// This package only builds sandboxed commands. Whether a caller refuses to run,
// asks the user, or falls back to an ordinary shell when the sandbox is
// unavailable is deliberately left to the caller.
package sandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnavailable indicates that the sandbox helper required by the current
// platform is not installed or cannot be found.
var ErrUnavailable = errors.New("sandbox is unavailable")

// Command is an executable plus its argument vector.
//
// Returning structured arguments instead of a newly assembled shell string is
// important even in a teaching implementation: it avoids sending the wrapper
// through another round of shell parsing. The original command is still shell
// text and is passed as the argument to the inner "bash -c" process.
type Command struct {
	Executable string
	Args       []string
}

// Config describes the small, common policy supported by both the Linux and
// macOS teaching backends.
type Config struct {
	// AllowWrite contains paths that commands may modify. The rest of the host
	// filesystem remains readable but is treated as read-only by the sandbox.
	AllowWrite []string

	// DenyWrite contains protected paths inside writable roots. A deny rule has
	// higher priority than an allow rule.
	DenyWrite []string

	// NetworkEnabled controls whether the command may use the host network.
	NetworkEnabled bool
}

// Sandbox converts shell text into a platform-specific sandbox command.
type Sandbox interface {
	// Available reports whether the helper used by this backend can be found.
	// It is intentionally only a lightweight check, not a complete guarantee
	// that every kernel or policy feature will work at execution time.
	Available() bool

	// Wrap returns the executable and arguments needed to run command inside
	// the sandbox described by config.
	Wrap(command string, config Config) (Command, error)
}

// New returns the sandbox implementation selected by Go build tags. Platforms
// other than Linux and macOS currently return nil.
func New() Sandbox {
	return newPlatformSandbox()
}

// normalizePaths makes policy paths absolute, cleans them and removes exact
// duplicates. Each OS backend still decides how those paths are enforced.
func normalizePaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))

	for _, configuredPath := range paths {
		if strings.TrimSpace(configuredPath) == "" {
			return nil, errors.New("sandbox path must not be empty")
		}

		absolutePath, err := filepath.Abs(configuredPath)
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox path %q: %w", configuredPath, err)
		}
		absolutePath = filepath.Clean(absolutePath)

		if _, exists := seen[absolutePath]; exists {
			continue
		}
		seen[absolutePath] = struct{}{}
		normalized = append(normalized, absolutePath)
	}

	return normalized, nil
}
