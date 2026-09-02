package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandNew(t *testing.T) {
	srv, store := newTestServer(t)
	reply := srv.handleMessage(context.Background(), "/new")
	if !strings.Contains(reply.Text, "new session") {
		t.Fatalf("/new reply = %q", reply.Text)
	}
	first, ok, _ := store.ActiveSession()
	if !ok || first.Agent {
		t.Fatalf("ActiveSession = %+v, ok %v; want a fresh chat session", first, ok)
	}
	srv.handleMessage(context.Background(), "/new")
	second, _, _ := store.ActiveSession()
	if second.ID == first.ID {
		t.Fatal("second /new did not switch the active session")
	}
}

func TestCommandSessions(t *testing.T) {
	srv, store := newTestServer(t)
	if reply := srv.handleMessage(context.Background(), "/sessions"); reply.Keyboard != nil || !strings.Contains(reply.Text, "no sessions") {
		t.Fatalf("/sessions on empty store = %+v", reply)
	}

	store.AddSession("a", false, "")
	store.AddSession("b", false, "")
	store.AddMessage("b", "user", strings.Repeat("x", 60), 1)
	store.AddSession("c", true, "claude")
	store.SetSessionName("c", "fix the bug")

	reply := srv.handleMessage(context.Background(), "/sessions")
	if len(reply.Keyboard) != 3 {
		t.Fatalf("keyboard rows = %d; want 3", len(reply.Keyboard))
	}
	if b := reply.Keyboard[0][0]; b.Text != "🤖 fix the bug" || b.CallbackData != "c" {
		t.Fatalf("row 0 = %+v; want flagged agent session c", b)
	}
	if b := reply.Keyboard[1][0]; !strings.HasPrefix(b.Text, "xxxx") || len([]rune(b.Text)) > 41 || b.CallbackData != "b" {
		t.Fatalf("row 1 = %+v; want first-message fallback trimmed to 40 runes", b)
	}
	if b := reply.Keyboard[2][0]; b.Text != "(empty)" || b.CallbackData != "a" {
		t.Fatalf("row 2 = %+v; want (empty) fallback", b)
	}
}

func TestCommandResume(t *testing.T) {
	srv, store := newTestServer(t)
	store.AddSession("a", false, "")
	store.SetSessionName("a", "trip planning")
	store.AddSession("b", false, "")
	store.AddPairing("telegram", "99", "CODE1234")
	store.ApprovePairing("telegram", "CODE1234")

	// approved sender resumes via button tap
	reply := srv.gatedCallback(context.Background(), 99, "a")
	if !strings.Contains(reply.Text, "resumed") || !strings.Contains(reply.Text, "trip planning") {
		t.Fatalf("resume reply = %q", reply.Text)
	}
	if sess, _, _ := store.ActiveSession(); sess.ID != "a" {
		t.Fatalf("active after resume = %+v; want a", sess)
	}

	if reply := srv.gatedCallback(context.Background(), 99, "nope"); !strings.Contains(reply.Text, "not found") {
		t.Fatalf("resume unknown = %q", reply.Text)
	}

	// unapproved senders get silence
	if reply := srv.gatedCallback(context.Background(), 42, "b"); reply.Text != "" {
		t.Fatalf("unapproved callback answered: %q", reply.Text)
	}
	if sess, _, _ := store.ActiveSession(); sess.ID != "a" {
		t.Fatal("unapproved callback moved the active session")
	}
}

func TestCommandAgentStart(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	reply := srv.handleMessage(context.Background(), "/agent")
	if !strings.Contains(reply.Text, "agent session") || !strings.Contains(reply.Text, "claude") {
		t.Fatalf("/agent reply = %q", reply.Text)
	}
	sess, ok, _ := store.ActiveSession()
	if !ok || !sess.Agent || sess.Provider != "claude" {
		t.Fatalf("ActiveSession = %+v, ok %v; want claude agent session", sess, ok)
	}

	// workspace seeded with instructions, both filenames
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(agentDir(), f)); err != nil {
			t.Fatalf("workspace %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(agentDir(), "chrome-profile")); err != nil {
		t.Fatalf("chrome-profile dir: %v", err)
	}

	// user edits are never clobbered
	custom := []byte("my own instructions")
	os.WriteFile(filepath.Join(agentDir(), "CLAUDE.md"), custom, 0o644)
	srv.handleMessage(context.Background(), "/agent")
	got, _ := os.ReadFile(filepath.Join(agentDir(), "CLAUDE.md"))
	if string(got) != string(custom) {
		t.Fatal("/agent overwrote an edited CLAUDE.md")
	}

	// openai default → codex-powered agent
	writeTestConfig(t, "default_llm: openai\n")
	srv.handleMessage(context.Background(), "/agent")
	if sess, _, _ := store.ActiveSession(); sess.Provider != "openai" {
		t.Fatalf("ActiveSession = %+v; want openai agent", sess)
	}

	// gemini default falls back to claude, with a note
	writeTestConfig(t, "default_llm: gemini\n")
	reply = srv.handleMessage(context.Background(), "/agent")
	if !strings.Contains(reply.Text, "gemini") || !strings.Contains(reply.Text, "claude") {
		t.Fatalf("gemini fallback reply = %q", reply.Text)
	}
	if sess, _, _ := store.ActiveSession(); sess.Provider != "claude" {
		t.Fatalf("ActiveSession = %+v; want claude fallback", sess)
	}
}
