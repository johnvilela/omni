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
