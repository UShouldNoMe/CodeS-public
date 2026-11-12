// Package workspacecatalog constructs tools bound to one agent's workspace.
package workspacecatalog

import (
	"CodeS/internal/sandbox"
	"CodeS/internal/tools"
	"CodeS/internal/tools/manager"
	"CodeS/internal/workspace"
	"fmt"
	"os"
	"path/filepath"
)

// New creates fresh tools. Isolated catalogs expose only tools whose workspace
// binding is known; external MCP tools are deliberately excluded.
func New(root string, isolated bool) (*manager.Manager, error) {
	ws, err := workspace.NewWorkspace(root)
	if err != nil {
		return nil, err
	}
	config := sandbox.Config{AllowWrite: []string{ws.Root(), os.TempDir()}}
	if isolated {
		ws = ws.WithProtectedGit()
		// Shared temp may contain another agent or the source repository.
		config.AllowWrite = []string{ws.Root()}
		config.DenyWrite = []string{filepath.Join(ws.Root(), ".git")}
	}
	bash := tools.NewBash(ws).WithSandbox(sandbox.New(), config)
	if isolated {
		bash = bash.RequireSandbox()
	}
	registry := manager.NewRegistry()
	for _, tool := range []tools.Tool{
		tools.CurrentOS{}, tools.Time{}, tools.NewListFile(ws), tools.NewReadFile(ws),
		tools.NewGlob(ws), tools.NewGrep(ws), tools.NewWriteFile(ws), tools.NewEditFile(ws), bash,
	} {
		if err := registry.Register(tool); err != nil {
			return nil, fmt.Errorf("register %s: %w", tool.Definition().Name, err)
		}
	}
	return registry, nil
}
