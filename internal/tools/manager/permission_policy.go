package manager

type PermissionPolicy struct {
	AllowedTools    map[string]struct{}
	DisallowedTools map[string]struct{}
	// 黑名单优先
}

func (p PermissionPolicy) Allows(name string) bool {
	if _, denied := p.DisallowedTools[name]; denied {
		return false
	}
	if len(p.AllowedTools) == 0 { // 有可能使用的模式是黑名单模式
		return true
	}
	_, allowed := p.AllowedTools[name]
	return allowed
}
