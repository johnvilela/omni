package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatContext(t *testing.T) {
	// budget 100: persona 25 tok, only the newest of two 50-tok messages fits
	persona := strings.Repeat("p", 100)
	history := []Message{
		{Content: strings.Repeat("a", 200)},
		{Content: strings.Repeat("b", 200)},
	}
	out := chatContext(persona, "", history, 100)
	for _, want := range []string{"budget 100", "75% (75 used)", "persona (AGENTS.md): 25", "messages: 50 (1/2 fit)", "free: 25"} {
		if !strings.Contains(out, want) {
			t.Fatalf("chatContext missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "memory:") {
		t.Fatalf("empty memory should have no line:\n%s", out)
	}
}

func TestAgentContext(t *testing.T) {
	t.Setenv("HOME", t.TempDir())                            // no ~/.claude files
	t.Setenv("XDG_DATA_HOME", t.TempDir())                   // no workspace seeds
	history := []Message{{Content: strings.Repeat("x", 80)}} // 20 tok

	// measured session: remainder = real total − estimates
	sess := Session{Agent: true, Provider: "claude", LastCtx: 1000}
	out := agentContext(sess, history)
	for _, want := range []string{"window 200.0k", "(1.0k used)", "messages: ~20", "system & MCP tools: ~980", "skills: ~0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("agentContext missing %q:\n%s", want, out)
		}
	}

	// no turn yet: estimates only, remainder unknown
	sess.LastCtx = 0
	out = agentContext(sess, history)
	if !strings.Contains(out, "system & MCP tools: ?") || !strings.Contains(out, "(20 used)") {
		t.Fatalf("unmeasured session render wrong:\n%s", out)
	}

	// codex: wider window, no skills line
	out = agentContext(Session{Agent: true, Provider: "openai", LastCtx: 1000}, nil)
	if !strings.Contains(out, "window 272.0k") || strings.Contains(out, "skills:") {
		t.Fatalf("codex render wrong:\n%s", out)
	}
}

// TestSkillTokensAgentWorkspace: plugin skills in the agent workspace count
// beside the user's ~/.claude/skills.
func TestSkillTokensAgentWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if got := skillTokens(); got != 0 {
		t.Fatalf("empty skillTokens = %d", got)
	}
	dir := filepath.Join(agentDir(), ".claude", "skills", "pec")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: pec\ndescription: finance\n---\nbody"), 0o644)
	if got := skillTokens(); got == 0 {
		t.Fatal("agent workspace skills not counted")
	}
}
