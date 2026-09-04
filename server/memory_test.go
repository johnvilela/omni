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

// mkMemoriaWiki fakes a bootstrapped memoria global wiki under the test's
// isolated XDG_CONFIG_HOME.
func mkMemoriaWiki(t *testing.T) string {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	wiki := filepath.Join(dir, "memoria", "wiki")
	if err := os.MkdirAll(wiki, 0o700); err != nil {
		t.Fatal(err)
	}
	return wiki
}

func TestCompaction(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	wiki := mkMemoriaWiki(t)
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	// 20 usable tokens on top of the constant tool-section overhead
	overhead := estTokens("\n\n" + cronPrompt(store) + "\n\n" + filePrompt() + "\n\n" + taskPrompt(store) + "\n\n" + plansPrompt() + "\n\n" + corePrompt(memoriaWiki(), Session{}))
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"),
		[]byte(fmt.Sprintf("token_budget: %d\n", overhead+20)), 0o600); err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("a", 100) // falls out of the 20-token budget
	if _, err := srv.ChatAnswer(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChatAnswer(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	page := filepath.Join(wiki, "omni-bot", "memory.md")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(page); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("memory page never written by compaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := readMemory(wiki); got != "pong" {
		t.Fatalf("memory body = %q; want digest reply", got)
	}

	// watermark = the max overflowed message id (the long user turn, id 1)
	for time.Now().Before(deadline) {
		if sess, _, _ := store.ActiveSession(); sess.ConsolidatedUntil == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sess, _, _ := store.ActiveSession(); sess.ConsolidatedUntil != 1 {
		t.Fatalf("consolidated_until = %d; want 1", sess.ConsolidatedUntil)
	}

	// the digest prompt carried the forgotten turn and the rewrite contract
	var digest string
	for _, p := range rec.all() {
		if strings.Contains(p, "Rewrite the complete page") {
			digest = p
		}
	}
	if digest == "" || !strings.Contains(digest, long) {
		t.Fatalf("digest prompt missing or without overflow: %q", digest)
	}
}

func TestCompactionEmptyDigest(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	wiki := mkMemoriaWiki(t)
	if err := writeMemory(wiki, "keep me"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSession("s1", false, ""); err != nil {
		t.Fatal(err)
	}
	id, err := store.AddMessage("s1", "user", "secret fact", 1)
	if err != nil {
		t.Fatal(err)
	}

	rec.reply = "" // llm returns nothing: never clobber the page
	srv.digesting.Store(true)
	srv.onCompaction("s1", []Message{{ID: id, Role: "user", Content: "secret fact"}})

	if got := readMemory(wiki); got != "keep me" {
		t.Fatalf("page clobbered by empty digest: %q", got)
	}
	if sess, _, _ := store.ActiveSession(); sess.ConsolidatedUntil != 0 {
		t.Fatalf("watermark moved on failed digest: %d", sess.ConsolidatedUntil)
	}
	if srv.digesting.Load() {
		t.Fatal("digest guard not released")
	}
}

func TestMemoriaWiki(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	// memoria not set up → ""
	if got := memoriaWiki(); got != "" {
		t.Fatalf("memoriaWiki(no memoria) = %q; want empty", got)
	}

	// default root: <config>/memoria/wiki
	def := filepath.Join(cfg, "memoria", "wiki")
	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := memoriaWiki(); got != def {
		t.Fatalf("memoriaWiki(default) = %q; want %q", got, def)
	}

	// global_path in memoria's config.yaml overrides the root
	custom := t.TempDir()
	if err := os.MkdirAll(filepath.Join(custom, "wiki"), 0o700); err != nil {
		t.Fatal(err)
	}
	yaml := "global_path: " + custom + "\n"
	if err := os.WriteFile(filepath.Join(cfg, "memoria", "config.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := memoriaWiki(); got != filepath.Join(custom, "wiki") {
		t.Fatalf("memoriaWiki(global_path) = %q; want %q", got, filepath.Join(custom, "wiki"))
	}
}

func TestStripFrontmatter(t *testing.T) {
	cases := []struct{ in, want string }{
		{"---\ntags: [omni-bot]\n---\n\nbody here", "body here"},
		{"no frontmatter", "no frontmatter"},
		{"---\ntags: [a, b]\n---\nbody", "body"},
	}
	for _, c := range cases {
		if got := stripFrontmatter(c.in); got != c.want {
			t.Fatalf("stripFrontmatter(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestMemoryReadWrite(t *testing.T) {
	wiki := t.TempDir()

	// missing page reads as empty
	if got := readMemory(wiki); got != "" {
		t.Fatalf("readMemory(missing) = %q; want empty", got)
	}

	if err := writeMemory(wiki, "owner likes go"); err != nil {
		t.Fatalf("writeMemory: %v", err)
	}
	// exact bytes: memoria-compatible frontmatter
	raw, err := os.ReadFile(filepath.Join(wiki, "omni-bot", "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "---\ntags: [omni-bot]\n---\n\nowner likes go" {
		t.Fatalf("page bytes = %q", raw)
	}
	if got := readMemory(wiki); got != "owner likes go" {
		t.Fatalf("readMemory = %q; want body only", got)
	}
}

// TestMemoryCommand walks the /memory path: condense in the background, park
// as a proposal, approval appends to the theme page; the theme index rides
// every prompt but the facts only after memory_load (general always rides).
func TestMemoryCommand(t *testing.T) {
	srv, store, rec, calls := newApprovalTestServer(t)
	wiki := mkMemoriaWiki(t)
	sess := seedSession(t, srv)

	rec.reply = "family|Owner's son is named Theo"
	if r := srv.handleMessage(context.Background(), "/memory my son is named Theo"); !strings.Contains(r.Text, "condensing") {
		t.Fatalf("/memory ack = %q", r.Text)
	}
	waitFor(t, func() bool { _, ok, _ := store.Proposal(1); return ok })
	p, _, _ := store.Proposal(1)
	if !strings.Contains(p.Reply, `"theme":"family"`) || !strings.Contains(p.Reply, "Theo") {
		t.Fatalf("proposal = %q; want themed memory_save", p.Reply)
	}
	if got := nextMessage(t, calls); !strings.Contains(got["text"].(string), "approval needed") {
		t.Fatalf("proposal push = %q", got["text"])
	}

	srv.gatedCallback(context.Background(), 42, "appr:1")
	waitFor(t, func() bool { return strings.Contains(readCoreTheme(wiki, "family"), "Theo") })

	// a NEW session (a work chat, say) sees the theme index but not the fact
	sess, err := srv.newSession(false, "")
	if err != nil {
		t.Fatal(err)
	}
	store.AddMessage(sess.ID, "user", "seed", 1)
	store.AddMessage(sess.ID, "assistant", "seeded", 2)
	rec.reply = "pong"
	if _, err := srv.chatAnswer(context.Background(), sess, "hello"); err != nil {
		t.Fatal(err)
	}
	prompts := rec.all()
	last := prompts[len(prompts)-1]
	if !strings.Contains(last, "family (1 facts, not loaded)") || strings.Contains(last, "Theo") {
		t.Fatalf("prompt = %q; want the index without the fact", last)
	}

	// load sticks to the session; facts ride the next prompt
	rec.reply = `TOOL:memory_load {"theme":"family"}`
	got, err := srv.chatAnswer(context.Background(), sess, "about my son...")
	if err != nil || !strings.Contains(got, "family memory loaded") {
		t.Fatalf("memory_load turn = %q, %v", got, err)
	}
	sess, _, _ = store.Session(sess.ID)
	if !strings.Contains(sess.Themes, "family") {
		t.Fatalf("session themes = %q; want family", sess.Themes)
	}
	rec.reply = "pong"
	if _, err := srv.chatAnswer(context.Background(), sess, "his name?"); err != nil {
		t.Fatal(err)
	}
	prompts = rec.all()
	if !strings.Contains(prompts[len(prompts)-1], "Theo") {
		t.Fatal("loaded theme's fact missing from the prompt")
	}

	// general facts ride every prompt with no load
	if err := appendCore(wiki, "general", "Owner prefers Portuguese"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.chatAnswer(context.Background(), sess, "oi"); err != nil {
		t.Fatal(err)
	}
	prompts = rec.all()
	if !strings.Contains(prompts[len(prompts)-1], "Owner prefers Portuguese") {
		t.Fatal("general fact missing from the prompt")
	}
}
