package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newToolsServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewServer(store, ""), store
}

func TestApplyTools(t *testing.T) {
	srv, store := newToolsServer(t)

	// no tool line: passthrough untouched
	if got := srv.applyTools(context.Background(), "just prose"); got != "just prose" {
		t.Fatalf("passthrough = %q", got)
	}

	// add
	got := srv.applyTools(context.Background(), `Sure!
TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"drink water"}`)
	if !strings.Contains(got, "Sure!") || !strings.Contains(got, "#1") || !strings.Contains(got, "drink water") {
		t.Fatalf("add reply = %q", got)
	}
	if cs, _ := store.Crons(); len(cs) != 1 || cs[0].Schedule != "0 8 * * *" {
		t.Fatalf("crons after add = %+v", cs)
	}

	// invalid schedule: error line, nothing stored
	got = srv.applyTools(context.Background(), `TOOL:cron_add {"schedule":"99 8 * * *","kind":"message","text":"x"}`)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("invalid schedule reply = %q", got)
	}
	// invalid kind
	got = srv.applyTools(context.Background(), `TOOL:cron_add {"schedule":"0 8 * * *","kind":"nuke","text":"x"}`)
	if !strings.Contains(got, "⚠") {
		t.Fatalf("invalid kind reply = %q", got)
	}
	if cs, _ := store.Crons(); len(cs) != 1 {
		t.Fatalf("invalid adds stored: %+v", cs)
	}

	// edit
	got = srv.applyTools(context.Background(), `TOOL:cron_edit {"id":1,"schedule":"30 8 * * *","kind":"message","text":"hydrate"}`)
	if !strings.Contains(got, "#1") {
		t.Fatalf("edit reply = %q", got)
	}
	if cs, _ := store.Crons(); cs[0].Text != "hydrate" || cs[0].Schedule != "30 8 * * *" {
		t.Fatalf("crons after edit = %+v", cs)
	}
	if got = srv.applyTools(context.Background(), `TOOL:cron_edit {"id":99,"schedule":"0 8 * * *","kind":"message","text":"x"}`); !strings.Contains(got, "⚠") {
		t.Fatalf("edit unknown = %q", got)
	}

	// delete
	if got = srv.applyTools(context.Background(), `TOOL:cron_delete {"id":1}`); !strings.Contains(got, "#1") {
		t.Fatalf("delete reply = %q", got)
	}
	if cs, _ := store.Crons(); len(cs) != 0 {
		t.Fatalf("crons after delete = %+v", cs)
	}
	if got = srv.applyTools(context.Background(), `TOOL:cron_delete {"id":1}`); !strings.Contains(got, "⚠") {
		t.Fatalf("delete gone = %q", got)
	}

	// a genuinely unknown tool still errs (file tools live in media_test.go)
	if got = srv.applyTools(context.Background(), `TOOL:nope {"x":1}`); got != "⚠ unknown tool nope" {
		t.Fatalf("unknown tool = %q", got)
	}
}

func TestCronPrompt(t *testing.T) {
	srv, store := newToolsServer(t)
	p := cronPrompt(srv.store)
	if !strings.Contains(p, "TOOL:cron_add") || !strings.Contains(p, "none yet") {
		t.Fatalf("empty cronPrompt = %q", p)
	}
	store.AddCron("0 8 * * *", "message", "drink water")
	p = cronPrompt(srv.store)
	if !strings.Contains(p, "#1") || !strings.Contains(p, "drink water") {
		t.Fatalf("cronPrompt with job = %q", p)
	}
}

// TestChatAnswerToolRound: the llm's TOOL line creates the cron and the
// stored/sent reply is the confirmation; the next prompt carries the job.
func TestChatAnswerToolRound(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	// this test covers the executor round; the approval gate has its own tests
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("approvals: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec.reply = `TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"drink water"}`
	got, err := srv.ChatAnswer(context.Background(), "remind me to drink water every day at 8am")
	if err != nil || !strings.Contains(got, "#1") || strings.Contains(got, "TOOL:") {
		t.Fatalf("ChatAnswer = %q, %v; want confirmation without the TOOL line", got, err)
	}
	if cs, _ := store.Crons(); len(cs) != 1 || cs[0].Text != "drink water" {
		t.Fatalf("crons = %+v", cs)
	}

	rec.reply = "you have one reminder"
	if _, err := srv.ChatAnswer(context.Background(), "what reminders do I have?"); err != nil {
		t.Fatal(err)
	}
	var sawJob, sawConfirmation bool
	for _, p := range rec.all() {
		if strings.Contains(p, "#1 · 0 8 * * *") {
			sawJob = true
		}
		if strings.Contains(p, "assistant: ⏰") {
			sawConfirmation = true
		}
	}
	if !sawJob {
		t.Fatalf("no prompt carried the cron list: %q", rec.all())
	}
	if !sawConfirmation {
		t.Fatalf("history carries no confirmation (raw TOOL line stored?): %q", rec.all())
	}
}
