package tui

// 模型菜单分两级：先选择配置中的提供商，再异步获取并选择该提供商的模型。
// provider 为空代表第一级，selected、loading、err 保存选择、加载和错误状态。
// 返回结果带有菜单指针，Update 据此忽略已经关闭或被替换的菜单结果。

import (
	"CodeS/internal/llm"
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type modelPicker struct {
	providers []string
	provider  string
	models    []llm.ModelInfo
	selected  int
	loading   bool
	err       string
	cancel    context.CancelFunc
}

type modelsLoadedMsg struct {
	picker *modelPicker
	models []llm.ModelInfo
	err    error
}

// 展示已配置的提供商，此时不获取网络模型列表。
func (m *Model) openModelPicker() (tea.Model, tea.Cmd) {
	if m.running || m.sessionBusy {
		m.status = "wait for the current operation to finish"
		return m, nil
	}
	m.modelPicker = &modelPicker{providers: m.app.ListProviders()}
	return m, nil
}

// 创建可取消上下文和新的菜单实例，在 Cmd 内读取模型列表。
func (m *Model) loadProviderModels(provider string) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(m.ctx)
	picker := &modelPicker{provider: provider, loading: true, cancel: cancel}
	m.modelPicker = picker
	app := m.app
	return m, func() tea.Msg {
		models, err := app.ListModels(ctx, provider)
		return modelsLoadedMsg{picker: picker, models: models, err: err}
	}
}

// 处理导航、返回、重试和确认，通过 App.SwitchModel 切换所选模型。
func (m *Model) handleModelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.modelPicker
	count := len(p.providers)
	if p.provider != "" {
		count = len(p.models)
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		if p.cancel != nil {
			p.cancel()
		}
		m.modelPicker = nil
		if msg.String() == "ctrl+c" {
			return m, m.exitWithUsage()
		}
		if p.provider != "" {
			return m.openModelPicker()
		}
	case "up":
		p.selected = max(0, p.selected-1)
	case "down":
		p.selected = max(0, min(count-1, p.selected+1))
	case "r":
		if p.provider != "" && !p.loading {
			return m.loadProviderModels(p.provider)
		}
	case "enter", "ctrl+j":
		if p.loading || count == 0 {
			return m, nil
		}
		if p.provider == "" {
			return m.loadProviderModels(p.providers[p.selected])
		}
		model := p.models[p.selected]
		// SwitchModel only constructs the client; no network request occurs here.
		if err := m.app.SwitchModel(m.ctx, model.Provider, model.ID); err != nil {
			p.err = err.Error()
			return m, nil
		}
		m.modelPicker = nil
		m.status = "model selected: " + resumeTitle(model.Provider+"/"+model.ID)
	}
	return m, nil
}

// 围绕选中项计算可见区间，再按终端宽高截断菜单输出。
func (m *Model) renderModelPicker() string {
	p := m.modelPicker
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	title := "Select provider"
	items := append([]string(nil), p.providers...)
	if p.provider != "" {
		title = "Select model · " + resumeTitle(p.provider)
		for _, model := range p.models {
			items = append(items, model.ID)
		}
	}
	lines := []string{title, "↑/↓ select · Enter confirm · Esc back · r retry", ""}
	switch {
	case p.loading:
		lines = append(lines, "Loading models…")
	case len(items) == 0:
		lines = append(lines, "No entries available.")
	default:
		rows := max(1, height-6)
		start := max(0, p.selected-rows+1)
		for i := start; i < min(len(items), start+rows); i++ {
			prefix := "  "
			if i == p.selected {
				prefix = "› "
			}
			lines = append(lines, prefix+resumeTitle(items[i]))
		}
		lines = append(lines, fmt.Sprintf("%d / %d", p.selected+1, len(items)))
	}
	if p.err != "" {
		lines = append(lines, "Error: "+resumeTitle(p.err))
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return strings.Join(lines, "\n")
}
