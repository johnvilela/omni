package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestAnswerNotice(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	got := srv.answerNotice(context.Background(), "hi")
	if !strings.HasPrefix(got, "⚠ ") || !strings.Contains(got, "llm connect") {
		t.Fatalf("answerNotice = %q; want ⚠ notice with hint", got)
	}
}
