package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeApprovalConfig replaces config.yaml under the test-scoped config dir.
func writeApprovalConfig(t *testing.T, cfg string) {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newApprovalTestServer is a chat test server with a recording telegram and
// one approved pairing (chat 42), so proposals land somewhere assertable.
func newApprovalTestServer(t *testing.T) (*Server, *Store, *promptRecorder, chan map[string]any) {
	t.Helper()
	srv, store, rec := newChatTestServer(t)
	if err := store.AddPairing("telegram", "42", "CODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePairing("telegram", "CODE"); err != nil {
		t.Fatal(err)
	}
	calls := make(chan map[string]any, 32)
	fake := fakePinAPI(t, calls)
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	return srv, store, rec, calls
}

// nextMessage waits for the next sendMessage, skipping typing indicators.
func nextMessage(t *testing.T, calls <-chan map[string]any) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case c := <-calls:
			if c["method"] == "sendMessage" {
				return c
			}
			if c["method"] != "sendChatAction" {
				t.Fatalf("unexpected call %v", c)
			}
		case <-deadline:
			t.Fatal("no sendMessage within 3s")
		}
	}
}

func TestGatedTools(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := gatedTools(); !slices.Equal(got, defaultApprovalTools) {
		t.Fatalf("gatedTools(no config) = %v; want default set", got)
	}
	writeApprovalConfig(t, "approvals: off\n")
	if got := gatedTools(); got != nil {
		t.Fatalf("gatedTools(off) = %v; want nil", got)
	}
	writeApprovalConfig(t, "approval_tools: [cron_add]\n")
	if got := gatedTools(); !slices.Equal(got, []string{"cron_add"}) {
		t.Fatalf("gatedTools(explicit) = %v; want [cron_add]", got)
	}
	writeApprovalConfig(t, "approval_skip: [write_file]\n")
	got := gatedTools()
	if slices.Contains(got, "write_file") || len(got) != len(defaultApprovalTools)-1 {
		t.Fatalf("gatedTools(skip) = %v; want default minus write_file", got)
	}
	writeApprovalConfig(t, "approval_tools: [cron_add, write_file]\napproval_skip: [cron_add]\n")
	if got := gatedTools(); !slices.Equal(got, []string{"write_file"}) {
		t.Fatalf("gatedTools(explicit+skip) = %v; want [write_file]", got)
	}
}

func TestGatedNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // default gated set
	free := "sure\nTOOL:read_file {\"path\":\"a.txt\"}\nTOOL:send_file {\"path\":\"a.txt\"}"
	if got := gatedNames(free); got != nil {
		t.Fatalf("gatedNames(free) = %v; want nil", got)
	}
	mixed := "doing it\nTOOL:write_file {\"path\":\"a\"}\nTOOL:cron_add {}\nTOOL:write_file {\"path\":\"b\"}"
	if got := gatedNames(mixed); !slices.Equal(got, []string{"write_file", "cron_add"}) {
		t.Fatalf("gatedNames(mixed) = %v; want deduped gated names", got)
	}
	// a line applyTools would skip (no space) must never be gated
	if got := gatedNames("TOOL:write_file"); got != nil {
		t.Fatalf("gatedNames(no-args line) = %v; want nil", got)
	}
}

// TestChatAnswerProposesGatedTool: a gated TOOL line executes nothing — the
// reply is parked as a proposal, history says so, and the owner gets buttons.
func TestChatAnswerProposesGatedTool(t *testing.T) {
	srv, store, rec, calls := newApprovalTestServer(t)
	sess := seedSession(t, srv)
	rec.reply = `TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"drink water"}`

	got, err := srv.chatAnswer(context.Background(), sess, "remind me daily")
	if err != nil || got != "" {
		t.Fatalf("chatAnswer = %q, %v; want out-of-band empty", got, err)
	}
	if cs, _ := store.Crons(); len(cs) != 0 {
		t.Fatalf("crons = %+v; nothing may run before approval", cs)
	}
	if _, ok, _ := store.Proposal(1); !ok {
		t.Fatal("no proposal row persisted")
	}
	ms, _ := store.Messages(sess.ID)
	last := ms[len(ms)-1].Content
	if !strings.Contains(last, "TOOL:cron_add") || !strings.Contains(last, "NOT executed") {
		t.Fatalf("stored turn = %q; want raw TOOL line + not-executed marker", last)
	}
	sent := nextMessage(t, calls)
	body, _ := json.Marshal(sent)
	if !strings.Contains(sent["text"].(string), "approval needed") {
		t.Fatalf("proposal text = %q", sent["text"])
	}
	for _, data := range []string{"appr:1", "alws:1", "deny:1", "edit:1"} {
		if !strings.Contains(string(body), data) {
			t.Fatalf("keyboard missing %s: %s", data, body)
		}
	}
}

// TestApproveProposalExecutes: tapping ✅ on a row seeded straight into the
// store (nothing in memory — the post-restart case) runs the TOOL lines once.
func TestApproveProposalExecutes(t *testing.T) {
	srv, store, _, calls := newApprovalTestServer(t)
	if _, err := store.AddProposal("sess1", `TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"water"}`); err != nil {
		t.Fatal(err)
	}

	r := srv.gatedCallback(context.Background(), 42, "appr:1")
	if r.Text != "✅ approved — running" || !r.StripKeyboard {
		t.Fatalf("approve ack = %+v; want ack with the buttons stripped", r)
	}
	waitFor(t, func() bool { cs, _ := store.Crons(); return len(cs) == 1 })
	if _, ok, _ := store.Proposal(1); ok {
		t.Fatal("approved row must be deleted")
	}
	ms, _ := store.Messages("sess1")
	if len(ms) != 1 || !strings.Contains(ms[0].Content, "owner approved proposal #1") ||
		!strings.Contains(ms[0].Content, "⏰ #1") {
		t.Fatalf("history = %+v; want the executed confirmation turn", ms)
	}
	if res := nextMessage(t, calls); !strings.Contains(res["text"].(string), "⏰ #1") {
		t.Fatalf("delivered result = %q", res["text"])
	}
	if r := srv.gatedCallback(context.Background(), 42, "appr:1"); r.Text != "⚠ proposal already resolved" || !r.StripKeyboard {
		t.Fatalf("second tap = %+v; a double tap must not execute twice", r)
	}
}

// TestApproveAlwaysWhitelists: ✅ always runs the proposal AND persists its
// tools into approval_skip, so the next use skips the gate.
func TestApproveAlwaysWhitelists(t *testing.T) {
	srv, store, rec, _ := newApprovalTestServer(t)
	sess := seedSession(t, srv)
	if _, err := store.AddProposal(sess.ID, `TOOL:write_file {"path":"x.txt","content":"hi"}`); err != nil {
		t.Fatal(err)
	}

	r := srv.gatedCallback(context.Background(), 42, "alws:1")
	if !strings.Contains(r.Text, "write_file") || !strings.Contains(r.Text, "won't ask again") {
		t.Fatalf("always ack = %q", r.Text)
	}
	if skip := readConfig().ApprovalSkip; !slices.Contains(skip, "write_file") {
		t.Fatalf("approval_skip = %v; want write_file persisted", skip)
	}
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(filesDir(), "x.txt"))
		return err == nil
	})

	rec.reply = `TOOL:write_file {"path":"y.txt","content":"again"}`
	got, err := srv.chatAnswer(context.Background(), sess, "write it again")
	if err != nil || !strings.Contains(got, "y.txt") || strings.Contains(got, "TOOL:") {
		t.Fatalf("whitelisted chatAnswer = %q, %v; want immediate execution", got, err)
	}
	if _, ok, _ := store.Proposal(2); ok {
		t.Fatal("whitelisted tool must not create a proposal")
	}
}

func TestDenyProposal(t *testing.T) {
	srv, store, _, _ := newApprovalTestServer(t)
	if _, err := store.AddProposal("sess1", `TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"water"}`); err != nil {
		t.Fatal(err)
	}

	r := srv.gatedCallback(context.Background(), 42, "deny:1")
	if r.Text != "🚫 denied — nothing was executed" || !r.StripKeyboard {
		t.Fatalf("deny ack = %+v; want ack with the buttons stripped", r)
	}
	if cs, _ := store.Crons(); len(cs) != 0 {
		t.Fatalf("crons = %+v; deny must execute nothing", cs)
	}
	if _, ok, _ := store.Proposal(1); ok {
		t.Fatal("denied row must be deleted")
	}
	ms, _ := store.Messages("sess1")
	if len(ms) != 1 || ms[0].Role != "user" || !strings.Contains(ms[0].Content, "denied proposal #1") {
		t.Fatalf("history = %+v; want the user denial turn", ms)
	}
}

func TestEditProposalReply(t *testing.T) {
	srv, store, _, _ := newApprovalTestServer(t)
	if _, err := store.AddProposal("sess1", `TOOL:cron_add {}`); err != nil {
		t.Fatal(err)
	}
	if r := srv.gatedCallback(context.Background(), 42, "edit:1"); !strings.Contains(r.Text, "send your changes") || r.StripKeyboard {
		t.Fatalf("edit ack = %+v; the proposal is still pending, buttons must stay", r)
	}
	if _, ok, _ := store.Proposal(1); !ok {
		t.Fatal("edit must keep the row — the next message supersedes it")
	}
	if r := srv.gatedCallback(context.Background(), 42, "edit:9"); r.Text != "⚠ proposal already resolved" {
		t.Fatalf("stale edit tap = %q", r.Text)
	}
}

// TestNewMessageSupersedesProposal: typing while a proposal is pending
// cancels it and leaves the cancellation in history before the new turn.
func TestNewMessageSupersedesProposal(t *testing.T) {
	srv, store, rec, _ := newApprovalTestServer(t)
	sess := seedSession(t, srv)
	if _, err := store.AddProposal(sess.ID, `TOOL:cron_add {}`); err != nil {
		t.Fatal(err)
	}

	rec.reply = "ok, dropped"
	got, err := srv.chatAnswer(context.Background(), sess, "actually don't")
	if err != nil || got != "ok, dropped" {
		t.Fatalf("chatAnswer = %q, %v", got, err)
	}
	if _, ok, _ := store.Proposal(1); ok {
		t.Fatal("pending proposal must be superseded")
	}
	ms, _ := store.Messages(sess.ID)
	var cancelAt, turnAt int
	for i, m := range ms {
		if strings.Contains(m.Content, "proposal cancelled") {
			cancelAt = i
		}
		if m.Content == "actually don't" {
			turnAt = i
		}
	}
	if cancelAt == 0 || turnAt == 0 || cancelAt > turnAt {
		t.Fatalf("history = %+v; want cancel note before the new turn", ms)
	}
}

// TestReadFileFollowupGated: the follow-up round after a read_file is built
// from file contents (untrusted input) — a gated TOOL line there must park
// like any other, with round 1 already persisted.
func TestReadFileFollowupGated(t *testing.T) {
	srv, store, rec, _ := newApprovalTestServer(t)
	sess := seedSession(t, srv)
	if err := os.MkdirAll(filesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir(), "notes.txt"), []byte("the secret is 42"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec.replies = []string{
		`TOOL:read_file {"path":"notes.txt"}`,
		"saving that\nTOOL:write_file {\"path\":\"out.txt\",\"content\":\"42\"}",
	}

	got, err := srv.chatAnswer(context.Background(), sess, "read my notes and save the secret")
	if err != nil || got != "" {
		t.Fatalf("chatAnswer = %q, %v; want out-of-band empty", got, err)
	}
	if _, err := os.Stat(filepath.Join(filesDir(), "out.txt")); err == nil {
		t.Fatal("gated round-2 write ran without approval")
	}
	p, ok, _ := store.Proposal(1)
	if !ok || !strings.Contains(p.Reply, "TOOL:write_file") {
		t.Fatalf("proposal = %+v, %v; want the round-2 reply parked", p, ok)
	}
	ms, _ := store.Messages(sess.ID)
	round1, round2 := ms[len(ms)-2].Content, ms[len(ms)-1].Content
	if !strings.Contains(round1, "the secret is 42") {
		t.Fatalf("round-1 turn = %q; want the executed 📄 dump", round1)
	}
	if !strings.Contains(round2, "NOT executed") {
		t.Fatalf("round-2 turn = %q; want the proposal marker", round2)
	}
}

func TestApprovalGateOff(t *testing.T) {
	srv, store, rec, _ := newApprovalTestServer(t)
	sess := seedSession(t, srv)
	writeApprovalConfig(t, "approvals: off\n")
	rec.reply = `TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"water"}`

	got, err := srv.chatAnswer(context.Background(), sess, "remind me")
	if err != nil || !strings.Contains(got, "#1") {
		t.Fatalf("chatAnswer = %q, %v; want immediate confirmation", got, err)
	}
	if cs, _ := store.Crons(); len(cs) != 1 {
		t.Fatalf("crons = %+v", cs)
	}
	if _, ok, _ := store.Proposal(1); ok {
		t.Fatal("gate off must not create proposals")
	}
}

// TestSaveConfigValue: merging one key keeps non-string values intact.
func TestSaveConfigValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	writeApprovalConfig(t, "token_budget: 9000\n")
	if err := saveConfigValue("approval_skip", []string{"write_file"}); err != nil {
		t.Fatal(err)
	}
	cfg := readConfig()
	if cfg.TokenBudget != 9000 || !slices.Contains(cfg.ApprovalSkip, "write_file") {
		t.Fatalf("config after save = %+v; want budget preserved + skip added", cfg)
	}
}
