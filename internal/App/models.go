package App

import (
	"CodeS/config"
	"CodeS/internal/llm"
	"CodeS/internal/llm/openai_compat"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ListProviders returns configuration order without exposing credentials to the UI.
func (a *App) ListProviders() []string {
	names := make([]string, 0, len(a.providers))
	for _, provider := range a.providers {
		names = append(names, provider.Name)
	}
	return names
}

func (a *App) providerConfig(name string) (config.ProviderConfig, error) {
	var match *config.ProviderConfig
	for _, provider := range a.providers {
		if provider.Name == name {
			if match != nil {
				return config.ProviderConfig{}, fmt.Errorf("duplicate provider name %q", name)
			}
			cpy := provider
			match = &cpy
		}
	}
	if match == nil {
		return config.ProviderConfig{}, fmt.Errorf("provider %q not found", name)
	}
	if match.Protocol != "openai-compatible" {
		return config.ProviderConfig{}, fmt.Errorf("provider %q: unsupported protocol %q", name, match.Protocol)
	}
	return *match, nil
}

func clientConfig(provider config.ProviderConfig, model string) openai_compat.Config {
	return openai_compat.Config{
		Provider: provider.Name,
		BaseURL:  provider.BaseURL,
		APIKey:   os.Getenv(provider.APIKeyEnv),
		Model:    model,
	}
}

// ListModels fetches only the selected provider, so another provider's failure
// cannot prevent this menu from loading.
func (a *App) ListModels(ctx context.Context, providerName string) ([]llm.ModelInfo, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	provider, err := a.providerConfig(providerName)
	if err != nil {
		return nil, err
	}
	catalog, err := openai_compat.NewModelCatalog(clientConfig(provider, ""))
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", providerName, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	models, err := catalog.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", providerName, err)
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	kept := make([]llm.ModelInfo, 0, len(a.models)+len(models))
	for _, model := range a.models {
		if model.Provider != providerName {
			kept = append(kept, model)
		}
	}
	a.models = append(kept, models...)
	return models, nil
}

func (a *App) SwitchModel(ctx context.Context, providerName, modelID string) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(modelID) == "" {
		return errors.New("model ID is empty")
	}
	provider, err := a.providerConfig(providerName)
	if err != nil {
		return err
	}
	client, err := openai_compat.New(clientConfig(provider, modelID))
	if err != nil {
		return err
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.agent.SetClient(client); err != nil {
		return err
	}
	a.currentClient = client
	a.currentModel = &llm.ModelInfo{Provider: providerName, ID: modelID, DisplayName: modelID}
	return nil
}

func (a *App) CurrentModel() *llm.ModelInfo {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	if a.currentModel == nil {
		return nil
	}
	model := *a.currentModel
	return &model
}
