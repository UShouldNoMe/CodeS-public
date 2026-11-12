package App

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildStoresProvidersWithoutCreatingLLMClient(t *testing.T) {
	workspaceRoot := t.TempDir()
	configPath := filepath.Join(workspaceRoot, "config.yaml")
	configContent := []byte(`providers:
  - name: deepseek
    protocol: openai-compatible
    base_url: https://api.deepseek.com/v1
    api_key_env: DEEPSEEK_API_KEY
  - name: local
    protocol: openai-compatible
    base_url: http://localhost:1234/v1
    api_key_env: LOCAL_API_KEY
mcp_servers: []
`)
	if err := os.WriteFile(configPath, configContent, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPSEEK_API_KEY", "")

	application, err := Build(context.Background(), BuildOptions{
		WorkspaceRoot: workspaceRoot,
		ConfigPath:    configPath,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Error(err)
		}
	})

	if len(application.providers) != 2 {
		t.Fatalf("providers count = %d, want 2", len(application.providers))
	}
	if got := application.providers[0]; got.Name != "deepseek" || got.Protocol != "openai-compatible" ||
		got.BaseURL != "https://api.deepseek.com/v1" || got.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("first provider = %#v", got)
	}
	if application.currentModel != nil {
		t.Fatalf("current model = %#v, want nil", application.currentModel)
	}
	if len(application.models) != 0 {
		t.Fatalf("models = %#v, want empty", application.models)
	}
}
