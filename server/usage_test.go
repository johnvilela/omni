package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, ok := claudeQuota(context.Background()); ok {
		t.Fatal("claudeQuota without creds = ok")
	}

	writeCreds(t, filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"five_hour":{"utilization":26.0,"resets_at":"2026-09-02T04:30:00+00:00"},
			"seven_day":{"utilization":9.0,"resets_at":"2026-09-07T23:00:00+00:00"}}`)
	}))
	t.Cleanup(fake.Close)
	t.Setenv("OMNI_CLAUDE_OAUTH_API", fake.URL)

	h5, d7, ok := claudeQuota(context.Background())
	if !ok || h5.Pct != 26 || d7.Pct != 9 || d7.Resets.UTC().Day() != 7 {
		t.Fatalf("claudeQuota = %+v, %+v, ok %v", h5, d7, ok)
	}
}

func TestCodexQuota(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, _, ok := codexQuota(); ok {
		t.Fatal("codexQuota without rollouts = ok")
	}

	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "09", "02")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := `{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":1.0,"window_minutes":300,"resets_at":1788314786},"secondary":{"used_percent":3.0,"window_minutes":10080,"resets_at":1788818386}}}}
{"type":"response_item"}
`
	if err := os.WriteFile(filepath.Join(dir, "rollout-a.jsonl"), []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	p, s, ok := codexQuota()
	if !ok || p.Pct != 1 || s.Pct != 3 || s.Resets.Unix() != 1788818386 {
		t.Fatalf("codexQuota = %+v, %+v, ok %v", p, s, ok)
	}
}

func TestMonthCosts(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/organization/costs"):
			fmt.Fprint(w, `{"data":[{"results":[{"amount":{"value":1.25}}]},{"results":[{"amount":{"value":0.75}}]}]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/organizations/cost_report"):
			fmt.Fprint(w, `{"data":[{"results":[{"amount":"2.50"}]}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	t.Setenv("OMNI_OPENAI_API", fake.URL)
	t.Setenv("OMNI_CLAUDE_API", fake.URL)

	if cost, ok := openaiMonthCost(context.Background(), "k"); !ok || cost != 2 {
		t.Fatalf("openaiMonthCost = %v, %v; want 2", cost, ok)
	}
	if cost, ok := claudeMonthCost(context.Background(), "k"); !ok || cost != 2.5 {
		t.Fatalf("claudeMonthCost = %v, %v; want 2.5", cost, ok)
	}

	// non-admin keys 401 → the line is omitted, never an error
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	t.Cleanup(denied.Close)
	t.Setenv("OMNI_OPENAI_API", denied.URL)
	if _, ok := openaiMonthCost(context.Background(), "k"); ok {
		t.Fatal("openaiMonthCost on 401 = ok")
	}
}

func TestCommandUsage(t *testing.T) {
	srv, store := newLLMTestServer(t)

	if reply := srv.handleMessage(context.Background(), "/usage"); !strings.Contains(reply.Text, "no llm providers") {
		t.Fatalf("/usage with nothing connected = %q", reply.Text)
	}

	t.Setenv("OPENAI_API_KEY", "GOOD")
	if _, code, err := srv.ConnectLLM(context.Background(), "openai", ""); code != 200 {
		t.Fatalf("connect = %d, %v", code, err)
	}
	store.AddUsage("openai", 1200, 300, 0, time.Now().Unix())
	store.AddUsage("gemini", 9, 9, 9, time.Now().Unix()) // disconnected: hidden

	reply := srv.handleMessage(context.Background(), "/usage")
	for _, want := range []string{"openai — api_key", "today: 1 req", "1.2k in / 300 out"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("/usage missing %q:\n%s", want, reply.Text)
		}
	}
	if strings.Contains(reply.Text, "gemini") || strings.Contains(reply.Text, "$") {
		t.Fatalf("/usage shows hidden provider or zero cost:\n%s", reply.Text)
	}

	// reported cost shows up
	store.AddUsage("openai", 100, 10, 0.42, time.Now().Unix())
	if reply := srv.handleMessage(context.Background(), "/usage"); !strings.Contains(reply.Text, "$0.42") {
		t.Fatalf("/usage missing cost:\n%s", reply.Text)
	}
}

func TestBar(t *testing.T) {
	for pct, want := range map[float64]string{
		0:   "░░░░░░░░░░",
		28:  "███░░░░░░░",
		100: "██████████",
		130: "██████████", // clamp
	} {
		if got := bar(pct); got != want {
			t.Fatalf("bar(%v) = %q; want %q", pct, got, want)
		}
	}
}

func TestFmtTok(t *testing.T) {
	for n, want := range map[int64]string{950: "950", 1200: "1.2k", 2_500_000: "2.5M"} {
		if got := fmtTok(n); got != want {
			t.Fatalf("fmtTok(%d) = %q; want %q", n, got, want)
		}
	}
}
