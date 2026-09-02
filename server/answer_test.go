package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAnswerNoDefault(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	_, err := srv.Answer(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "llm connect") {
		t.Fatalf("Answer with no default = %v; want llm connect hint", err)
	}
}

func TestAnswerDisconnectedDefault(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("default_llm: claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := srv.Answer(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("Answer with disconnected default = %v; want not connected", err)
	}
}

func TestAnswerAPIKey(t *testing.T) {
	envs := map[string]string{"openai": "OPENAI_API_KEY", "claude": "ANTHROPIC_API_KEY", "gemini": "GEMINI_API_KEY"}
	for _, p := range llmProviders {
		t.Run(p, func(t *testing.T) {
			srv, _ := newLLMTestServer(t)
			t.Setenv(envs[p], "GOOD")
			if _, code, err := srv.ConnectLLM(context.Background(), p, ""); code != 200 {
				t.Fatalf("connect = %d, %v", code, err)
			}
			got, err := srv.Answer(context.Background(), "ping")
			if err != nil || got != "pong" {
				t.Fatalf("Answer = %q, %v; want pong", got, err)
			}
		})
	}
}

func TestAnswerClaudeCode(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	bin := t.TempDir()
	script := "#!/bin/sh\necho '{\"result\":\"pong\",\"session_id\":\"s\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if _, code, err := srv.ConnectLLM(context.Background(), "claude", ""); code != 200 {
		t.Fatalf("connect = %d, %v", code, err)
	}
	got, err := srv.Answer(context.Background(), "ping")
	if err != nil || got != "pong" {
		t.Fatalf("Answer via claude binary = %q, %v; want pong", got, err)
	}
}

// chatFakeScript returns a fake vendor CLI that records its args and answers
// pong the way the real one does: claude as a json result, codex as JSONL
// events plus the final message in the -o file, gemini as plain text.
func chatFakeScript(bin, argsFile string) string {
	switch bin {
	case "claude":
		return "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho '{\"result\":\"pong\",\"session_id\":\"s\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2}}'\n"
	case "codex":
		return "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + `
prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
[ -n "$out" ] && echo pong > "$out"
echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}'
`
	}
	return "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho pong\n"
}

// dropOPair removes "-o <path>" (the unpredictable temp file) from recorded
// args before comparison.
func dropOPair(t *testing.T, args []string) []string {
	t.Helper()
	i := slices.Index(args, "-o")
	if i < 0 {
		t.Fatalf("args %q missing -o last-message file", args)
	}
	return slices.Delete(args, i, i+2)
}

// TestAnswerCLIBare locks the security contract of every vendor CLI chat
// call: no MCP servers, no user config/settings, tools disabled or
// read-only. The host's agent setup must never leak into chat answers.
// claude/codex additionally carry structured-output flags so usage can be
// recorded — those do not loosen the bare contract.
func TestAnswerCLIBare(t *testing.T) {
	cases := []struct {
		provider, rel, creds, bin string
		dropO                     bool
		want                      []string
	}{
		{"openai", filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`, "codex", true,
			[]string{"exec", "--skip-git-repo-check", "--ignore-user-config", "-s", "read-only", "--json", "ping"}},
		{"claude", filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`, "claude", false,
			[]string{"-p", "ping", "--tools", "", "--strict-mcp-config", "--setting-sources", "", "--output-format", "json"}},
		{"gemini", filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"tok"}`, "gemini", false,
			[]string{"--allowed-mcp-server-names", "omni-none", "-e", "none", "-p", "ping"}},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv, _ := newLLMTestServer(t)
			writeCreds(t, c.rel, c.creds)
			bin := t.TempDir()
			argsFile := filepath.Join(bin, "args.txt")
			if err := os.WriteFile(filepath.Join(bin, c.bin), []byte(chatFakeScript(c.bin, argsFile)), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if _, code, err := srv.ConnectLLM(context.Background(), c.provider, ""); code != 200 {
				t.Fatalf("connect = %d, %v", code, err)
			}
			if got, err := srv.Answer(context.Background(), "ping"); err != nil || got != "pong" {
				t.Fatalf("Answer = %q, %v; want pong", got, err)
			}
			got := readLines(t, argsFile)
			if c.dropO {
				got = dropOPair(t, got)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("cli args = %q; want %q", got, c.want)
			}
		})
	}
}

func writeTestConfig(t *testing.T, content string) {
	t.Helper()
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAnswerConfiguredModelCLI asserts a user-picked model reaches each vendor
// CLI as its model flag without disturbing the bare-invocation contract.
func TestAnswerConfiguredModelCLI(t *testing.T) {
	cases := []struct {
		provider, rel, creds, bin string
		dropO                     bool
		want                      []string
	}{
		{"openai", filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`, "codex", true,
			[]string{"exec", "--skip-git-repo-check", "--ignore-user-config", "-s", "read-only", "-m", "m-test", "--json", "ping"}},
		{"claude", filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`, "claude", false,
			[]string{"-p", "ping", "--tools", "", "--strict-mcp-config", "--setting-sources", "", "--model", "m-test", "--output-format", "json"}},
		{"gemini", filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"tok"}`, "gemini", false,
			[]string{"--allowed-mcp-server-names", "omni-none", "-e", "none", "-m", "m-test", "-p", "ping"}},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv, _ := newLLMTestServer(t)
			writeCreds(t, c.rel, c.creds)
			writeTestConfig(t, c.provider+"_model: m-test\n")
			bin := t.TempDir()
			argsFile := filepath.Join(bin, "args.txt")
			if err := os.WriteFile(filepath.Join(bin, c.bin), []byte(chatFakeScript(c.bin, argsFile)), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if _, code, err := srv.ConnectLLM(context.Background(), c.provider, ""); code != 200 {
				t.Fatalf("connect = %d, %v", code, err)
			}
			if got, err := srv.Answer(context.Background(), "ping"); err != nil || got != "pong" {
				t.Fatalf("Answer = %q, %v; want pong", got, err)
			}
			got := readLines(t, argsFile)
			if c.dropO {
				got = dropOPair(t, got)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("cli args = %q; want %q", got, c.want)
			}
		})
	}
}

// TestAnswerRecordsUsage: every answered call logs its reported token usage
// for /usage — api_key path here, CLI paths covered by the bare tests' fakes.
func TestAnswerRecordsUsage(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	if _, code, err := srv.ConnectLLM(context.Background(), "openai", ""); code != 200 {
		t.Fatalf("connect = %d, %v", code, err)
	}
	if _, err := srv.Answer(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	u, err := store.UsageSince("openai", 0)
	if err != nil || u.Requests != 1 || u.In != 7 || u.Out != 3 {
		t.Fatalf("UsageSince = %+v, %v; want the fake's 7 in / 3 out", u, err)
	}
}

// TestAnswerConfiguredModelAPI asserts the api_key path sends the configured
// model: the fake endpoint rejects every other model name.
func TestAnswerConfiguredModelAPI(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "m-test" {
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	})
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	t.Setenv("OMNI_OPENAI_API", fake.URL)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	writeTestConfig(t, "openai_model: m-test\n")

	if _, code, err := srv.ConnectLLM(context.Background(), "openai", ""); code != 200 {
		t.Fatalf("connect = %d, %v", code, err)
	}
	got, err := srv.Answer(context.Background(), "ping")
	if err != nil || got != "pong" {
		t.Fatalf("Answer = %q, %v; want pong with the configured model", got, err)
	}
}

// TestRunCLIErrorConcise: a failing CLI's stderr can echo the whole composed
// prompt (codex does) — only the last line may reach the user-visible error.
func TestRunCLIErrorConcise(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'Conversation so far: secret history' >&2\necho 'ERROR: model not supported' >&2\nexit 1\n"
	path := filepath.Join(bin, "failer")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(context.Background(), "", time.Minute, path)
	if err == nil || !strings.Contains(err.Error(), "ERROR: model not supported") {
		t.Fatalf("err = %v; want the last stderr line", err)
	}
	if strings.Contains(err.Error(), "secret history") {
		t.Fatalf("err = %v; earlier stderr lines must not leak", err)
	}
}

// TestAnswerWithProvider: an explicit provider wins over the default; an
// explicit but disconnected provider errors with the connect hint.
func TestAnswerWithProvider(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	t.Setenv("ANTHROPIC_API_KEY", "GOOD")
	for _, p := range []string{"openai", "claude"} {
		if _, code, err := srv.ConnectLLM(context.Background(), p, ""); code != 200 {
			t.Fatalf("connect %s = %d, %v", p, code, err)
		}
	}
	writeTestConfig(t, "default_llm: claude\n")
	// distinct reply proves the openai endpoint was the one hit
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"openai-pong"}}]}`)
	}))
	t.Cleanup(fake.Close)
	t.Setenv("OMNI_OPENAI_API", fake.URL)

	if got, err := srv.answerWith(context.Background(), "openai", "ping"); err != nil || got != "openai-pong" {
		t.Fatalf("answerWith(openai) = %q, %v; want openai-pong", got, err)
	}
	if got, err := srv.Answer(context.Background(), "ping"); err != nil || got != "pong" {
		t.Fatalf("Answer (default claude) = %q, %v; want pong", got, err)
	}
	if _, err := srv.answerWith(context.Background(), "gemini", "ping"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("answerWith(disconnected) = %v; want not connected", err)
	}
}

func TestAnswerNotice(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	got := srv.answerNotice(context.Background(), "hi")
	if !strings.HasPrefix(got, "⚠ ") || !strings.Contains(got, "llm connect") {
		t.Fatalf("answerNotice = %q; want ⚠ notice with hint", got)
	}
}
