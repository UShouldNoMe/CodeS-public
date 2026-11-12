package manager

import (
	"CodeS/internal/tools"
	"context"
	"fmt"
	"sync"
)

type Request struct {
	ToolName  string
	Authority tools.Category
	Arguments map[string]any
}

type Approver func(context.Context, Request) (bool, error)

type ExecutionMode string

const (
	ModeManual     ExecutionMode = "manual"
	ModeFullAccess ExecutionMode = "full_access"
)

func (m ExecutionMode) Validate() error {
	if m != ModeManual && m != ModeFullAccess {
		return fmt.Errorf("invalid execution mode %q: expected manual or full_access", m)
	}
	return nil
}

type PermissionManager struct {
	mu       sync.RWMutex
	mode     ExecutionMode
	approver Approver
	policy   PermissionPolicy
}

func New(approver Approver) *PermissionManager {
	return &PermissionManager{approver: approver, mode: ModeManual}
}

func NewWithPolicy(approver Approver, policy PermissionPolicy) *PermissionManager {
	return &PermissionManager{approver: approver, policy: clonePolicy(policy), mode: ModeManual}
}

func (m *PermissionManager) Authorize(ctx context.Context, def tools.ToolDef, arg map[string]any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.RLock()
	policy, mode, approver := m.policy, m.mode, m.approver
	m.mu.RUnlock()
	if !policy.Allows(def.Name) {
		return false, nil
	}
	switch def.Authority {
	case tools.CategoryRead:
		{
			return true, nil
		}
	case tools.CategoryWrite, tools.CategoryCommand:
		{
			if mode == ModeFullAccess {
				return true, nil
			}
			if approver == nil {
				return false, nil
			}
			return approver(ctx, Request{
				ToolName:  def.Name,
				Authority: def.Authority,
				Arguments: arg,
			})
		}
	default:
		{
			return false, fmt.Errorf("tool %q has invalid permission category %q",
				def.Name,
				def.Authority)
		}
	}

}

func (m *PermissionManager) ExecutionMode() ExecutionMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.mode == "" {
		return ModeManual
	}
	return m.mode
}
func (m *PermissionManager) SetExecutionMode(mode ExecutionMode) error {
	if err := mode.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = mode
	return nil
}

// Derive copies tool restrictions without carrying an interactive approver.
func (m *PermissionManager) Derive(mode ExecutionMode) (*PermissionManager, error) {
	if err := mode.Validate(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return &PermissionManager{policy: clonePolicy(m.policy), mode: mode}, nil
}
func clonePolicy(policy PermissionPolicy) PermissionPolicy {
	result := PermissionPolicy{AllowedTools: make(map[string]struct{}, len(policy.AllowedTools)), DisallowedTools: make(map[string]struct{}, len(policy.DisallowedTools))}
	for name := range policy.AllowedTools {
		result.AllowedTools[name] = struct{}{}
	}
	for name := range policy.DisallowedTools {
		result.DisallowedTools[name] = struct{}{}
	}
	return result
}
