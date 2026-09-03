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

func TestCommandCrons(t *testing.T) {
	srv, store := newTestServer(t)
	if reply := srv.handleMessage(context.Background(), "/crons"); reply.Keyboard != nil || !strings.Contains(reply.Text, "no scheduled jobs") {
		t.Fatalf("/crons empty = %+v", reply)
	}

	store.AddCron("0 8 * * *", "message", "drink water")
	store.AddCron("0 9 * * 3", "agent", "scan for gigs")
	reply := srv.handleMessage(context.Background(), "/crons")
	for _, want := range []string{"#1", "0 8 * * *", "drink water", "#2", "agent", "scan for gigs"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("/crons missing %q:\n%s", want, reply.Text)
		}
	}
	if len(reply.Keyboard) != 2 || reply.Keyboard[0][0].CallbackData != "cron-del:1" ||
		!strings.Contains(reply.Keyboard[1][0].Text, "🗑") {
		t.Fatalf("/crons keyboard = %+v", reply.Keyboard)
	}

	// delete via button tap (approved sender), prefix routed apart from
	// resume; the reply is the refreshed listing, edited onto the tapped
	// message so the surviving buttons stay live
	store.AddPairing("telegram", "99", "CODE1234")
	store.ApprovePairing("telegram", "CODE1234")
	r := srv.gatedCallback(context.Background(), 99, "cron-del:2")
	if !r.Edit || strings.Contains(r.Text, "#2") || !strings.Contains(r.Text, "#1") || len(r.Keyboard) != 1 {
		t.Fatalf("cron delete callback = %+v; want the pruned listing edited in place", r)
	}
	if cs, _ := store.Crons(); len(cs) != 1 || cs[0].ID != 1 {
		t.Fatalf("crons after delete = %+v", cs)
	}
	// a stale tap heals the outdated listing the same way
	if r := srv.gatedCallback(context.Background(), 99, "cron-del:2"); !r.Edit || !strings.Contains(r.Text, "#1") {
		t.Fatalf("stale delete tap = %+v; want the current listing", r)
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

	// user edits are never clobbered; missing contract sections are appended
	custom := []byte("my own instructions")
	os.WriteFile(filepath.Join(agentDir(), "CLAUDE.md"), custom, 0o644)
	srv.handleMessage(context.Background(), "/agent")
	got, _ := os.ReadFile(filepath.Join(agentDir(), "CLAUDE.md"))
	if !strings.HasPrefix(string(got), string(custom)) || !strings.Contains(string(got), "TOOL:send_file") {
		t.Fatal("/agent must keep owner edits and append the send_file contract")
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

// TestCommandAgentAtProvider: /agent @provider overrides the default; invalid
// picks fail before any session is created.
func TestCommandAgentAtProvider(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	reply := srv.handleMessage(context.Background(), "/agent @openai")
	if !strings.Contains(reply.Text, "openai") {
		t.Fatalf("/agent @openai reply = %q", reply.Text)
	}
	sess, ok, _ := store.ActiveSession()
	if !ok || !sess.Agent || sess.Provider != "openai" {
		t.Fatalf("ActiveSession = %+v, ok %v; want openai agent", sess, ok)
	}

	// explicit but unsupported provider: loud error, no session created
	reply = srv.handleMessage(context.Background(), "/agent @gemini do it")
	if !strings.Contains(reply.Text, "⚠") || !strings.Contains(reply.Text, "not supported") {
		t.Fatalf("/agent @gemini reply = %q", reply.Text)
	}
	if after, _, _ := store.ActiveSession(); after.ID != sess.ID {
		t.Fatal("/agent @gemini created a session")
	}

	// unknown provider: loud error, no session created
	reply = srv.handleMessage(context.Background(), "/agent @foo do it")
	if !strings.Contains(reply.Text, "⚠") || !strings.Contains(reply.Text, "unknown provider") {
		t.Fatalf("/agent @foo reply = %q", reply.Text)
	}
	if after, _, _ := store.ActiveSession(); after.ID != sess.ID {
		t.Fatal("/agent @foo created a session")
	}
}

// TestCommandChatAtProvider: @provider in plain chat is sticky — it moves the
// active chat session onto that provider until /new or another pick.
func TestCommandChatAtProvider(t *testing.T) {
	srv, store, _ := newChatTestServer(t) // openai connected; answers "pong"

	// bare @openai switches without answering
	reply := srv.handleMessage(context.Background(), "@openai")
	if !strings.Contains(reply.Text, "openai") {
		t.Fatalf("@openai reply = %q", reply.Text)
	}
	sess, _, _ := store.ActiveSession()
	if sess.Agent || sess.Provider != "openai" {
		t.Fatalf("ActiveSession = %+v; want chat session on openai", sess)
	}

	// with text the rest is queued for the picked provider (silent ack); the
	// answer lands in the session in the background
	reply = srv.handleMessage(context.Background(), "@openai ping")
	if reply.Text != "" {
		t.Fatalf("@openai ping = %q; want queued silence", reply.Text)
	}
	waitFor(t, func() bool {
		ms, _ := store.Messages(sess.ID)
		return len(ms) == 2 && ms[1].Content == "pong"
	})
	waitFor(t, func() bool { return !srv.running(sess.ID) })

	// unknown provider: error, nothing saved to the session
	before, _ := store.Messages(sess.ID)
	reply = srv.handleMessage(context.Background(), "@foo hi")
	if !strings.Contains(reply.Text, "unknown provider") {
		t.Fatalf("@foo reply = %q", reply.Text)
	}
	if after, _ := store.Messages(sess.ID); len(after) != len(before) {
		t.Fatal("@foo saved a message")
	}

	// /new resets to the default provider
	srv.handleMessage(context.Background(), "/new")
	if sess, _, _ := store.ActiveSession(); sess.Provider != "" {
		t.Fatalf("session after /new = %+v; want no provider pin", sess)
	}
}

// TestCommandAtProviderInAgentSession: an agent session's provider is fixed
// at /agent time — @picks point the user at a new agent session instead.
func TestCommandAtProviderInAgentSession(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store.AddSession("s1", true, "claude")
	store.SetActiveSession("s1")

	reply := srv.handleMessage(context.Background(), "@openai hi")
	if !strings.Contains(reply.Text, "/agent @openai") {
		t.Fatalf("@pick in agent session = %q; want a /agent hint", reply.Text)
	}
	if sess, _, _ := store.Session("s1"); sess.Provider != "claude" {
		t.Fatal("@pick changed the agent session's provider")
	}
}
