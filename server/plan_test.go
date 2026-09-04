package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestPlanFlow walks the whole /plan path: interview contract injected, a
// TOOL:ask turn becomes an options keyboard, a tapped option answers like
// typed text, plan_save parks as a proposal, approval writes the wiki page
// (and nothing starts), plan_start on a #long plan registers the daily cron.
func TestPlanFlow(t *testing.T) {
	srv, store, rec, calls := newApprovalTestServer(t)
	wiki := mkMemoriaWiki(t)
	sess := seedSession(t, srv)

	rec.reply = "Great goal!\nTOOL:ask {\"question\":\"What pace?\",\"options\":[\"slow\",\"fast\"]}"
	if r := srv.handleMessage(context.Background(), "/plan run a marathon"); !strings.Contains(r.Text, "planning mode") {
		t.Fatalf("/plan ack = %q", r.Text)
	}
	if got, _, _ := store.Session(sess.ID); !got.Plan {
		t.Fatal("session not flagged as planning")
	}
	ask := nextMessage(t, calls)
	body, _ := json.Marshal(ask)
	if !strings.Contains(ask["text"].(string), "What pace?") || !strings.Contains(string(body), "opt:slow") {
		t.Fatalf("ask push = %s; want question + option buttons", body)
	}
	found := false
	for _, p := range rec.all() {
		found = found || strings.Contains(p, "## Planning mode")
	}
	if !found {
		t.Fatal("no recorded prompt carries the planning contract")
	}

	rec.reply = `TOOL:plan_save {"title":"Run a Marathon","tags":["long"],"body":"## Goal\nrun 42km\n\n## Steps\n1. train\n\n## Target\nfinish the race\n\n## Progress\n"}`
	if r := srv.gatedCallback(context.Background(), 42, "opt:slow"); r.Text != "▶ slow" || !r.StripKeyboard {
		t.Fatalf("option tap = %+v; want echo with buttons stripped", r)
	}
	waitFor(t, func() bool { _, ok, _ := store.Proposal(1); return ok })
	page := planPath(wiki, "run-a-marathon")
	if _, err := os.Stat(page); err == nil {
		t.Fatal("plan written before approval")
	}

	srv.gatedCallback(context.Background(), 42, "appr:1")
	waitFor(t, func() bool { _, err := os.Stat(page); return err == nil })
	raw, _ := os.ReadFile(page)
	if !strings.Contains(string(raw), "status: active") || !strings.Contains(string(raw), "long") {
		t.Fatalf("plan page = %q; want active status + long tag", raw)
	}
	waitFor(t, func() bool { got, _, _ := store.Session(sess.ID); return !got.Plan })
	if cs, _ := store.Crons(); len(cs) != 0 {
		t.Fatalf("crons = %+v; approval must not start the plan", cs)
	}
	if ts, _ := store.Tasks(); len(ts) != 0 {
		t.Fatalf("tasks = %+v; approval must not start the plan", ts)
	}

	out := srv.runTool(context.Background(), "", "plan_start", `{"slug":"run-a-marathon"}`)
	if !strings.Contains(out, "daily agent job") {
		t.Fatalf("plan_start = %q", out)
	}
	cs, _ := store.Crons()
	if len(cs) != 1 || cs[0].Kind != "agent" || !strings.Contains(cs[0].Text, page) {
		t.Fatalf("crons = %+v; want one daily agent job naming the plan page", cs)
	}
	if out := srv.runTool(context.Background(), "", "plan_start", `{"slug":"nope"}`); !strings.Contains(out, "not found") {
		t.Fatalf("plan_start unknown = %q", out)
	}
}

// TestPlanCronDone: a daily plan run whose reply ends with PLAN DONE removes
// its own cron and tells the owner.
func TestPlanCronDone(t *testing.T) {
	srv, store := newTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	fake := `#!/bin/sh
printf '%s\n' '{"result":"trained today\nPLAN DONE","session_id":"v1"}'
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	sent := make(chan map[string]any, 2)
	tgFake := fakeTelegram(t, sent, nil, nil, "")
	t.Cleanup(tgFake.Close)
	srv.mu.Lock()
	srv.tg = NewTelegram(tgFake.URL, "TOKEN")
	srv.mu.Unlock()
	store.AddPairing("telegram", "42", "CODE1111")
	store.ApprovePairing("telegram", "CODE1111")

	id, err := store.AddCron("0 9 * * *", "agent", dailyPlanPrompt("/tmp/plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	srv.fireCron(context.Background(), Cron{ID: id, Schedule: "0 9 * * *", Kind: "agent", Text: dailyPlanPrompt("/tmp/plan.md")})

	select {
	case body := <-sent:
		if !strings.Contains(body["text"].(string), "plan complete") {
			t.Fatalf("delivered = %q; want the completion note", body["text"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no cron delivery within 3s")
	}
	if cs, _ := store.Crons(); len(cs) != 0 {
		t.Fatalf("crons = %+v; the finished plan's job must be removed", cs)
	}
}

func TestPlanDone(t *testing.T) {
	if !planDone("did things\n\nPLAN DONE\n") {
		t.Fatal("last-line PLAN DONE not detected")
	}
	if planDone("PLAN DONE was mentioned\nmore text") {
		t.Fatal("non-final mention must not trigger")
	}
}
