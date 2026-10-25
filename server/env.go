package main

import (
	"os"
	"path/filepath"
	"strings"
)

// Child processes — vendor clis in chat and agent mode, plugin commands,
// /terminal shells, "$" one-shots — get an allowlisted environment, never
// os.Environ(): the server's env carries the bot token, the api token,
// ai-memory's token and every provider key, and an agent steered by a
// malicious page would read them with one `env`. Each vendor cli gets only
// its own provider's variables.

// childEnvAllow are exact names or prefixes (trailing _) that any child may
// see: what a shell, node, chromium and the clis need to run at all.
var childEnvAllow = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "PWD", "EDITOR",
	"LANG", "LANGUAGE", "LC_", "TZ",
	"TMPDIR", "TMP", "TEMP",
	"XDG_", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	"NO_COLOR", "NODE_", "NPM_CONFIG_", "DISABLE_AUTOUPDATER",
	"OMNI_CONTAINER", "OMNI_AI_MEMORY_URL", "OMNI_ADDR",
}

// vendorEnvAllow adds one vendor's own variables (login, key, model
// overrides) when that vendor runs. Keys are the cli basename.
var vendorEnvAllow = map[string][]string{
	"claude": {"ANTHROPIC_", "CLAUDE_"},
	"codex":  {"OPENAI_", "CODEX_"},
	"gemini": {"GEMINI_", "GOOGLE_"},
}

// childEnv is the environment for a shell or a non-vendor child.
func childEnv() []string { return vendorEnv("") }

// vendorEnv is childEnv plus the named vendor's variables; vendor is a cli
// path or basename ("" = none).
func vendorEnv(vendor string) []string {
	allow := childEnvAllow
	if extra, ok := vendorEnvAllow[filepath.Base(vendor)]; ok {
		allow = append(append([]string{}, allow...), extra...)
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if envAllowed(name, allow) {
			env = append(env, kv)
		}
	}
	return env
}

func envAllowed(name string, allow []string) bool {
	for _, a := range allow {
		if name == a || (strings.HasSuffix(a, "_") && strings.HasPrefix(name, a)) {
			return true
		}
	}
	return false
}
