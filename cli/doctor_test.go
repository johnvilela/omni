package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	dir := filepath.Join(tmp, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigChecks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cs := configChecks()
	if len(cs) != 1 || !cs[0].skip {
		t.Fatalf("missing config = %+v, want one skip", cs)
	}

	writeConfig(t, "telegram_token: 123:abc\ndefault_llm: claude\n")
	cs = configChecks()
	if failCount(cs) != 0 {
		t.Fatalf("valid config = %+v, want no failures", cs)
	}

	writeConfig(t, "telegram_token: [broken\n")
	cs = configChecks()
	if failCount(cs) != 1 || !strings.Contains(cs[0].name, "ignores") {
		t.Fatalf("malformed yaml = %+v, want one failure warning the file is ignored", cs)
	}

	writeConfig(t, "token_budget: lots\n")
	if cs = configChecks(); failCount(cs) != 1 {
		t.Fatalf("bad token_budget type = %+v, want one failure", cs)
	}

	writeConfig(t, "default_llm: cohere\n")
	cs = configChecks()
	if failCount(cs) != 1 || !strings.Contains(cs[len(cs)-1].fix, "set-default") {
		t.Fatalf("unknown default_llm = %+v, want a set-default fix", cs)
	}
}

func TestLLMCheck(t *testing.T) {
	got := llmCheck([]LLM{{Name: "openai", Connected: true, Default: true}})
	if !got.ok {
		t.Errorf("connected default should pass: %+v", got)
	}
	got = llmCheck([]LLM{{Name: "openai", Default: true}, {Name: "claude", Connected: true}})
	if got.ok || !strings.Contains(got.fix, "connect -p openai") {
		t.Errorf("disconnected default should fail with a connect fix: %+v", got)
	}
	got = llmCheck([]LLM{{Name: "openai"}, {Name: "claude"}})
	if got.ok || !strings.Contains(got.fix, "llm connect") {
		t.Errorf("nothing connected should fail with a connect fix: %+v", got)
	}
	got = llmCheck([]LLM{{Name: "openai", Connected: true}, {Name: "claude", Connected: true}})
	if got.ok || !strings.Contains(got.fix, "set-default") {
		t.Errorf("no default should fail with a set-default fix: %+v", got)
	}
}

func TestRenderSectionAndFailCount(t *testing.T) {
	cs := []check{
		{name: "good", ok: true},
		{name: "bad", fix: "run this", info: "extra line"},
		{name: "meh", skip: true},
	}
	if failCount(cs) != 1 {
		t.Fatalf("failCount = %d, want 1 (skip must not count)", failCount(cs))
	}
	out := renderSection("TEST", cs)
	for _, want := range []string{"TEST", "good", "bad", "run this", "extra line", "meh", "✓", "✗", "–"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}
}

func TestServerChecks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			fmt.Fprintf(w, `{"app":%q,"version":%q}`, app, version)
		case "/channels":
			fmt.Fprint(w, `[{"name":"telegram","connected":true}]`)
		case "/llm":
			fmt.Fprint(w, `[{"name":"claude","connected":true,"default":true}]`)
		case "/pairing/telegram":
			fmt.Fprint(w, `[{"user_id":"1","approved":true}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	c := &Client{Base: srv.URL, http: &http.Client{Timeout: 5 * time.Second}}
	if cs := serverChecks(c); failCount(cs) != 0 {
		t.Errorf("healthy server = %+v, want no failures", cs)
	}

	srv.Close()
	cs := serverChecks(c)
	if failCount(cs) != 1 || !cs[1].skip {
		t.Errorf("dead server = %+v, want one failure plus a skip line", cs)
	}
	if !strings.Contains(cs[0].fix, "restart") {
		t.Errorf("dead server fix should suggest a restart: %+v", cs[0])
	}
}
