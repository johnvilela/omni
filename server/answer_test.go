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
	script := "#!/bin/sh\necho pong\n"
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

// TestAnswerCLIBare locks the security contract of every vendor CLI call:
// no MCP servers, no user config/settings, tools disabled or read-only. The
// host's agent setup must never leak into chat answers.
func TestAnswerCLIBare(t *testing.T) {
	cases := []struct {
		provider, rel, creds, bin string
		want                      []string
	}{
		{"openai", filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`, "codex",
			[]string{"exec", "--skip-git-repo-check", "--ignore-user-config", "-s", "read-only", "ping"}},
		{"claude", filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`, "claude",
			[]string{"-p", "ping", "--tools", "", "--strict-mcp-config", "--setting-sources", ""}},
		{"gemini", filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"tok"}`, "gemini",
			[]string{"--allowed-mcp-server-names", "omni-none", "-e", "none", "-p", "ping"}},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv, _ := newLLMTestServer(t)
			writeCreds(t, c.rel, c.creds)
			bin := t.TempDir()
			argsFile := filepath.Join(bin, "args.txt")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho pong\n"
			if err := os.WriteFile(filepath.Join(bin, c.bin), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if _, code, err := srv.ConnectLLM(context.Background(), c.provider, ""); code != 200 {
				t.Fatalf("connect = %d, %v", code, err)
			}
			if got, err := srv.Answer(context.Background(), "ping"); err != nil || got != "pong" {
				t.Fatalf("Answer = %q, %v; want pong", got, err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != strings.Join(c.want, "\n")+"\n" {
				t.Fatalf("cli args = %q; want %q", raw, c.want)
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
		want                      []string
	}{
		{"openai", filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`, "codex",
			[]string{"exec", "--skip-git-repo-check", "--ignore-user-config", "-s", "read-only", "-m", "m-test", "ping"}},
		{"claude", filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`, "claude",
			[]string{"-p", "ping", "--tools", "", "--strict-mcp-config", "--setting-sources", "", "--model", "m-test"}},
		{"gemini", filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"tok"}`, "gemini",
			[]string{"--allowed-mcp-server-names", "omni-none", "-e", "none", "-m", "m-test", "-p", "ping"}},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv, _ := newLLMTestServer(t)
			writeCreds(t, c.rel, c.creds)
			writeTestConfig(t, c.provider+"_model: m-test\n")
			bin := t.TempDir()
			argsFile := filepath.Join(bin, "args.txt")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho pong\n"
			if err := os.WriteFile(filepath.Join(bin, c.bin), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if _, code, err := srv.ConnectLLM(context.Background(), c.provider, ""); code != 200 {
				t.Fatalf("connect = %d, %v", code, err)
			}
			if got, err := srv.Answer(context.Background(), "ping"); err != nil || got != "pong" {
				t.Fatalf("Answer = %q, %v; want pong", got, err)
			}
			raw, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != strings.Join(c.want, "\n")+"\n" {
				t.Fatalf("cli args = %q; want %q", raw, c.want)
			}
		})
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

func TestAnswerNotice(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	got := srv.answerNotice(context.Background(), "hi")
	if !strings.HasPrefix(got, "⚠ ") || !strings.Contains(got, "llm connect") {
		t.Fatalf("answerNotice = %q; want ⚠ notice with hint", got)
	}
}
