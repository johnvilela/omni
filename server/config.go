package main

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	TelegramToken string `yaml:"telegram_token"`
	OpenAIKey     string `yaml:"openai_key"`
	AnthropicKey  string `yaml:"anthropic_key"`
	GeminiKey     string `yaml:"gemini_key"`
	DefaultLLM    string `yaml:"default_llm"`
	OpenAIModel   string `yaml:"openai_model"`
	ClaudeModel   string `yaml:"claude_model"`
	GeminiModel   string `yaml:"gemini_model"`
	OpenAIEffort  string `yaml:"openai_effort"`
	ClaudeEffort  string `yaml:"claude_effort"`
	GeminiEffort  string `yaml:"gemini_effort"`
	TokenBudget   int    `yaml:"token_budget"` // chat history budget in est. tokens; 0 = default
	// approval gate over privileged chat TOOL lines (server/approval.go);
	// Approvals is a string so the zero value (unreadable config) stays gated
	Approvals     string   `yaml:"approvals"`      // "off" disables the gate; anything else = on
	ApprovalTools []string `yaml:"approval_tools"` // TOOL names needing approval; unset = default privileged set
	ApprovalSkip  []string `yaml:"approval_skip"`  // whitelisted via ✅ always — subtracted from the gated set
}

// readConfig loads ~/.config/omni/config.yaml; zero Config on any error.
func readConfig() Config {
	var cfg Config
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return Config{}
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return Config{}
	}
	return cfg
}

// saveConfigValue merges one key into config.yaml (0600), keeping other keys
// intact. Twin of cli/main.go saveConfigKey; any-valued so non-string keys
// (token_budget, approval_skip) survive the round-trip.
func saveConfigValue(key string, v any) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		yaml.Unmarshal(data, &cfg) // unreadable config: start fresh
	}
	cfg[key] = v
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// ConfigPath is ~/.config/omni/config.yaml (respects XDG_CONFIG_HOME).
func ConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, app, "config.yaml")
}

// ResolveToken picks the Telegram bot token: explicit request value first,
// then TELEGRAM_BOT_TOKEN env, then ~/.config/omni/config.yaml.
func ResolveToken(req string) string {
	if req != "" {
		return req
	}
	if env := os.Getenv("TELEGRAM_BOT_TOKEN"); env != "" {
		return env
	}
	return readConfig().TelegramToken
}
