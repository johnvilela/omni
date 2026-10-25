package main

import (
	"os"
	"strings"
)

// secretEnvPrefixes are variables no child process (vendor cli, /terminal
// shell, "$" one-shot) may inherit from the server: the bot token, the api
// token, ai-memory's token and provider keys. SEC-02 narrows this further to
// an allowlist per vendor.
var secretEnvPrefixes = []string{
	"TELEGRAM_BOT_TOKEN=", "OMNI_API_TOKEN=", "OMNI_AI_MEMORY_TOKEN=",
	"ANTHROPIC_API_KEY=", "OPENAI_API_KEY=", "GEMINI_API_KEY=", "GOOGLE_API_KEY=",
}

// childEnv is os.Environ() minus the secrets above.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		drop := false
		for _, p := range secretEnvPrefixes {
			if strings.HasPrefix(kv, p) {
				drop = true
				break
			}
		}
		if !drop {
			env = append(env, kv)
		}
	}
	return env
}
