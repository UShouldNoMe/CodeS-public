package manager

import (
	"CodeS/internal/tools"
	"errors"
	"sync"
)

type Manager struct {
	mu             sync.RWMutex
	tools          map[string]tools.Tool
	toolsWithOrder []string
}

type ToolCatalog interface {
	Get(string) (tools.Tool, error)
	Definitions() []tools.ToolDef
}

func NewRegistry() *Manager {
	return new(Manager{
		tools:          make(map[string]tools.Tool),
		toolsWithOrder: make([]string, 0, 100),
	})
}

func (r *Manager) Register(tool tools.Tool) error {
	if tool == nil {
		return errors.New("tool is nil")
	}
	if tool.Definition().Name == "" {
		return errors.New("tool has no name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[tool.Definition().Name]; ok {
		return errors.New("tool " + tool.Definition().Name + " has already been registered")
	}
	r.tools[tool.Definition().Name] = tool
	r.toolsWithOrder = append(r.toolsWithOrder, tool.Definition().Name)
	return nil
}

func (r *Manager) Get(name string) (tools.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if tool, ok := r.tools[name]; !ok {
		return nil, errors.New("no such tool " + name)
	} else {
		return tool, nil
	}
}

func (r *Manager) Definitions() []tools.ToolDef {
	ret := make([]tools.ToolDef, 0, len(r.toolsWithOrder))
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, tool := range r.toolsWithOrder {
		ret = append(ret, r.tools[tool].Definition())
	}
	return ret
}
