package manager

import (
	"CodeS/internal/tools"
	"errors"
	"fmt"
)

// 原始ToolCatalog可以暴露所有工具 这个是给subAgent准备的
// 目的是限制subAgent能看到的工具数量

var ErrToolNotAllowed = errors.New("tools is not allowed")

type FilteredCatalog struct {
	source ToolCatalog
	policy PermissionPolicy
}

func NewFilteredCatalog(source ToolCatalog, policy PermissionPolicy) *FilteredCatalog {
	return &FilteredCatalog{
		source: source,
		policy: policy,
	}
}

func (f *FilteredCatalog) Definitions() []tools.ToolDef {
	definitions := f.source.Definitions()
	ret := make([]tools.ToolDef, 0, len(definitions))
	for _, toolDef := range definitions {
		if f.policy.Allows(toolDef.Name) {
			ret = append(ret, toolDef)
		}
	}
	return ret
}

func (f *FilteredCatalog) Get(name string) (tools.Tool, error) {
	if !f.policy.Allows(name) {
		return nil, fmt.Errorf("%w: %s", ErrToolNotAllowed, name)
	}
	return f.source.Get(name)
}
