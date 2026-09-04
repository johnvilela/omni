package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePluginFixture installs a fake plugin: a manifest snapshot in the data
// dir and a script "binary" in ~/.local/bin. printf, never echo (dash).
func writePluginFixture(t *testing.T, script string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	if err := os.MkdirAll(pluginsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"pecunia","version":"0.4.0","description":"finance",` +
		`"mcp":{"command":"pecunia","args":["mcp"]},` +
		`"commands":[{"name":"pecunia_today","description":"today summary","argv":["pecunia","today"]}]}`
	if err := os.WriteFile(filepath.Join(pluginsDir(), "pecunia.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "pecunia"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPluginCommandDispatch: a declared command execs the plugin binary with
// its argv plus the user's args and relays stdout; /pecunia-today (hyphen)
// aliases /pecunia_today.
func TestPluginCommandDispatch(t *testing.T) {
	srv, _ := newTestServer(t)
	writePluginFixture(t, "#!/bin/sh\nprintf 'ran %s' \"$*\"\n")

	reply := srv.handleMessage(context.Background(), "/pecunia_today extra args")
	if reply.Text != "ran today extra args" {
		t.Fatalf("dispatch reply = %q", reply.Text)
	}
	reply = srv.handleMessage(context.Background(), "/pecunia-today")
	if reply.Text != "ran today" {
		t.Fatalf("hyphen alias reply = %q", reply.Text)
	}
}

// TestPluginCommandError: a failing plugin command surfaces as a ⚠ notice.
func TestPluginCommandError(t *testing.T) {
	srv, _ := newTestServer(t)
	writePluginFixture(t, "#!/bin/sh\nprintf 'boom\\n' >&2\nexit 1\n")

	reply := srv.handleMessage(context.Background(), "/pecunia_today")
	if !strings.HasPrefix(reply.Text, "⚠") || !strings.Contains(reply.Text, "boom") {
		t.Fatalf("error reply = %q", reply.Text)
	}
}

// TestPluginCommandEmptyOutput: silence from the binary still answers.
func TestPluginCommandEmptyOutput(t *testing.T) {
	srv, _ := newTestServer(t)
	writePluginFixture(t, "#!/bin/sh\nexit 0\n")

	reply := srv.handleMessage(context.Background(), "/pecunia_today")
	if reply.Text != "✓ (no output)" {
		t.Fatalf("empty-output reply = %q", reply.Text)
	}
}

// TestPluginCommandUnknown: an unmatched slash command is not a plugin reply —
// handleMessage falls through to the session answer path.
func TestPluginCommandUnknown(t *testing.T) {
	srv, _ := newTestServer(t)
	writePluginFixture(t, "#!/bin/sh\n")

	if _, ok := srv.pluginReply(context.Background(), "/nope", ""); ok {
		t.Fatal("pluginReply claimed an unknown command")
	}
}

// TestPluginAgentText: the composed first message is the declared prompt,
// the owner's raw trailing words (punctuation intact), the plans directory
// and the scheduled-jobs contract with the live job list.
func TestPluginAgentText(t *testing.T) {
	_, store := newToolsServer(t)
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "memoria", "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	store.AddCron("0 9 * * *", "message", "[pecunia-coach] check in")

	c := pluginCommand{Name: "pecunia_coach", Prompt: "You are the coach."}
	got := pluginAgentText(c, "spent $12,50 on lunch!", store)
	if !strings.HasPrefix(got, "You are the coach.") {
		t.Fatalf("prompt not first: %q", got)
	}
	for _, want := range []string{
		"Owner's message: spent $12,50 on lunch!",
		filepath.Join(cfg, "memoria", "wiki", "omni-bot", "plans"),
		"## Scheduled jobs",
		"[pecunia-coach] check in",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composed text missing %q: %q", want, got)
		}
	}
	if got := pluginAgentText(c, "", store); strings.Contains(got, "Owner's message:") {
		t.Fatalf("empty arg still rendered an owner message: %q", got)
	}
}

// TestPluginPromptCommandDispatch: a prompt-declared command starts an agent
// session and queues the composed prompt instead of exec'ing anything.
func TestPluginPromptCommandDispatch(t *testing.T) {
	srv, store := newTestServer(t)
	writePluginFixture(t, "#!/bin/sh\n")
	manifest := `{"name":"pecunia","version":"0.6.0","description":"finance",` +
		`"commands":[{"name":"pecunia_coach","description":"coach","prompt":"You are the coach."}]}`
	if err := os.WriteFile(filepath.Join(pluginsDir(), "pecunia.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // the drain's vendor CLI exec must fail fast

	reply := srv.handleMessage(context.Background(), "/pecunia-coach spent $12, forgot to log")
	if !strings.Contains(reply.Text, "⏳ /pecunia_coach") {
		t.Fatalf("prompt dispatch reply = %q", reply.Text)
	}
	sess, ok, _ := store.ActiveSession()
	if !ok || !sess.Agent {
		t.Fatalf("ActiveSession = %+v, ok %v; want agent session", sess, ok)
	}
}

// TestPluginTgCommands: manifests become setMyCommands entries.
func TestPluginTgCommands(t *testing.T) {
	writePluginFixture(t, "#!/bin/sh\n")
	got := pluginTgCommands()
	if len(got) != 1 || got[0]["command"] != "pecunia_today" || got[0]["description"] == "" {
		t.Fatalf("pluginTgCommands = %v", got)
	}
}

// TestPluginsSyncEndpoint: POST /plugins/sync re-publishes the telegram menu
// including plugin commands; without a connected poller it still answers 200.
func TestPluginsSyncEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	writePluginFixture(t, "#!/bin/sh\n")

	// no poller yet: best-effort, published false
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/plugins/sync", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"published":false`) {
		t.Fatalf("sync without tg = %d %s", rec.Code, rec.Body.String())
	}

	// with a live telegram, the menu is re-published with the plugin command
	got := make(chan map[string]any, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/setMyCommands", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got <- body
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	fake := httptest.NewServer(mux)
	defer fake.Close()
	srv.mu.Lock()
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	srv.mu.Unlock()

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/plugins/sync", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"published":true`) {
		t.Fatalf("sync with tg = %d %s", rec.Code, rec.Body.String())
	}
	cmds, _ := (<-got)["commands"].([]any)
	last := cmds[len(cmds)-1].(map[string]any)
	if last["command"] != "pecunia_today" {
		t.Fatalf("last registered command = %v; want pecunia_today", last)
	}
}
