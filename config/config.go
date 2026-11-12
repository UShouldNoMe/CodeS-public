package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	ExecutionMode string            `yaml:"execution_mode"`
	Providers     []ProviderConfig  `yaml:"providers"`
	MCPServers    []MCPServerConfig `yaml:"mcp_servers"`
}

type ProviderConfig struct {
	Name      string `yaml:"name"`
	Protocol  string `yaml:"protocol"`
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type MCPServerConfig struct {
	Name     string        `yaml:"name"`
	Endpoint string        `yaml:"endpoint"`
	Auth     MCPAuthConfig `yaml:"auth"`
}

type MCPAuthConfig struct {
	Type     string `yaml:"type"`
	TokenEnv string `yaml:"token_env"`
}

func Load(path string) (*AppConfig, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ret AppConfig
	if err := yaml.Unmarshal(file, &ret); err != nil {
		return nil, err
	}
	return &ret, nil
}
