package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestChildEnvAllowlist (SEC-02): children see PATH/HOME/XDG and their own
// vendor's variables, never the bot token, api token or other providers' keys.
func TestChildEnvAllowlist(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "AUDIT_BOT")
	t.Setenv("OMNI_API_TOKEN", "AUDIT_API")
	t.Setenv("OMNI_AI_MEMORY_TOKEN", "AUDIT_MEM")
	t.Setenv("ANTHROPIC_API_KEY", "AUDIT_ANTHROPIC")
	t.Setenv("OPENAI_API_KEY", "AUDIT_OPENAI")
	t.Setenv("GEMINI_API_KEY", "AUDIT_GEMINI")
	t.Setenv("AUDIT_PROVIDER_SECRET", "AUDIT_RANDOM")
	t.Setenv("XDG_DATA_HOME", "/x")

	has := func(env []string, name string) bool {
		return slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, name+"=") })
	}
	for _, c := range []struct {
		vendor string
		allow  []string
		deny   []string
	}{
		{"", []string{"PATH", "HOME", "XDG_DATA_HOME"}, []string{"TELEGRAM_BOT_TOKEN", "OMNI_API_TOKEN", "OMNI_AI_MEMORY_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "AUDIT_PROVIDER_SECRET"}},
		{"/usr/bin/claude", []string{"ANTHROPIC_API_KEY"}, []string{"OPENAI_API_KEY", "GEMINI_API_KEY", "TELEGRAM_BOT_TOKEN"}},
		{"codex", []string{"OPENAI_API_KEY"}, []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "OMNI_API_TOKEN"}},
		{"gemini", []string{"GEMINI_API_KEY"}, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}},
	} {
		env := vendorEnv(c.vendor)
		for _, n := range c.allow {
			if !has(env, n) {
				t.Errorf("%q: %s missing", c.vendor, n)
			}
		}
		for _, n := range c.deny {
			if has(env, n) {
				t.Errorf("%q: %s leaked", c.vendor, n)
			}
		}
	}

	// end to end: a child printing a server secret prints nothing
	out, err := runCLI(context.Background(), "", 5*time.Second, "/bin/sh", "-c", "printf '%s%s' \"$AUDIT_PROVIDER_SECRET\" \"$TELEGRAM_BOT_TOKEN\"")
	if err != nil || out != "" {
		t.Fatalf("child saw secrets: %q %v", out, err)
	}
}

// TestAgentArgsModes: restricted is the default; agent_permissions: full is
// the only way to the dangerous flags.
func TestAgentArgsModes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if agentFullPermissions() {
		t.Fatal("no config: must be restricted")
	}
	os.MkdirAll(agentDir(), 0o700)
	os.WriteFile(filepath.Join(agentDir(), ".mcp.json"), []byte(`{"mcpServers":{"pecunia":{"command":"x"}}}`), 0o644)

	c := claudeAgentArgs("hi", "", false)
	joined := strings.Join(c, " ")
	if slices.Contains(c, "--dangerously-skip-permissions") || !strings.Contains(joined, "--permission-prompts none") ||
		!strings.Contains(joined, "mcp__pecunia") || !strings.Contains(joined, "Bash(playwright-cli *)") || !slices.Contains(c, "--mcp-config") {
		t.Fatalf("restricted claude = %q", c)
	}
	if c := claudeAgentArgs("hi", "v1", true); !slices.Contains(c, "--dangerously-skip-permissions") || slices.Contains(c, "--permission-mode") || c[len(c)-1] != "v1" {
		t.Fatalf("full claude = %q", c)
	}
	if x := codexAgentArgs("hi", "", "/tmp/o", false); slices.Contains(x, "--dangerously-bypass-approvals-and-sandbox") || !slices.Contains(x, "workspace-write") {
		t.Fatalf("restricted codex = %q", x)
	}
	if x := codexAgentArgs("hi", "v1", "/tmp/o", true); !slices.Contains(x, "--dangerously-bypass-approvals-and-sandbox") || x[1] != "resume" {
		t.Fatalf("full codex = %q", x)
	}

	if err := saveConfigValue("agent_permissions", "full"); err != nil {
		t.Fatal(err)
	}
	if !agentFullPermissions() {
		t.Fatal("agent_permissions: full not honoured")
	}
}
