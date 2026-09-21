package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompactionWritesAIMemoryPage(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	memory, pages := newAIMemoryFake(t)
	srv.aiMemory = memory
	dir, _ := os.UserConfigDir()
	os.MkdirAll(filepath.Join(dir, app), 0o700)
	overhead := estTokens("\n\n" + cronPrompt(store) + "\n\n" + filePrompt() + "\n\n" + taskPrompt(store) +
		"\n\n" + srv.plansPrompt(context.Background()) + "\n\n" + srv.corePrompt(context.Background(), Session{}))
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"),
		[]byte(fmt.Sprintf("token_budget: %d\n", overhead+20)), 0o600); err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("a", 100)
	if _, err := srv.ChatAnswer(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChatAnswer(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Contains(pages.Get(memoryPage), "pong") })

	sess, _, _ := store.ActiveSession()
	if sess.ConsolidatedUntil != 1 {
		t.Fatalf("consolidated_until = %d; want 1", sess.ConsolidatedUntil)
	}
	var digest string
	for _, p := range rec.all() {
		if strings.Contains(p, "Rewrite the complete page") {
			digest = p
		}
	}
	if !strings.Contains(digest, long) {
		t.Fatalf("digest prompt = %q", digest)
	}
}

func TestCompactionEmptyDigestKeepsPage(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	memory, pages := newAIMemoryFake(t)
	srv.aiMemory = memory
	pages.Set(memoryPage, "# Omni long-term memory\n\nkeep me")
	store.AddSession("s1", false, "")
	id, _ := store.AddMessage("s1", "user", "secret fact", 1)
	rec.reply = ""
	srv.digesting.Store(true)
	srv.onCompaction("s1", []Message{{ID: id, Role: "user", Content: "secret fact"}})
	if !strings.Contains(pages.Get(memoryPage), "keep me") {
		t.Fatalf("page clobbered: %q", pages.Get(memoryPage))
	}
	if srv.digesting.Load() {
		t.Fatal("digest guard not released")
	}
}

func TestStripFrontmatter(t *testing.T) {
	if got := stripFrontmatter("---\ntags: [a]\n---\n\nbody"); got != "body" {
		t.Fatalf("stripFrontmatter = %q", got)
	}
}

func TestMemoryCommandKeepsApprovalExperience(t *testing.T) {
	srv, store, rec, calls := newApprovalTestServer(t)
	memory, pages := newAIMemoryFake(t)
	srv.aiMemory = memory
	seedSession(t, srv)
	rec.reply = "family|Owner's son is named Theo"
	if r := srv.handleMessage(context.Background(), "/memory my son is named Theo"); !strings.Contains(r.Text, "condensing") {
		t.Fatalf("ack = %q", r.Text)
	}
	waitFor(t, func() bool { _, ok, _ := store.Proposal(1); return ok })
	p, _, _ := store.Proposal(1)
	if !strings.Contains(p.Reply, `"theme":"family"`) {
		t.Fatalf("proposal = %q", p.Reply)
	}
	if got := nextMessage(t, calls); !strings.Contains(got["text"].(string), "approval needed") {
		t.Fatalf("proposal push = %q", got["text"])
	}
	srv.gatedCallback(context.Background(), 42, "appr:1")
	waitFor(t, func() bool { return strings.Contains(pages.Get(corePath("family")), "Theo") })
}

func TestMemoryRetentionDefaultsToThirtyDays(t *testing.T) {
	if got := memoryRetentionDays(Config{}); got != 30 {
		t.Fatalf("default retention = %d", got)
	}
	if got := memoryRetentionDays(Config{MemoryRetentionDays: 45}); got != 45 {
		t.Fatalf("configured retention = %d", got)
	}
}

func TestOnCompactionReturnsQuicklyWhenMemoryUnavailable(t *testing.T) {
	srv, store, _ := newChatTestServer(t)
	store.AddSession("s1", false, "")
	srv.digesting.Store(true)
	start := time.Now()
	srv.onCompaction("s1", nil)
	if time.Since(start) > time.Second {
		t.Fatal("unavailable memory blocked compaction")
	}
}
