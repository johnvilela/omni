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

func TestAnswerNotice(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	got := srv.answerNotice(context.Background(), "hi")
	if !strings.HasPrefix(got, "⚠ ") || !strings.Contains(got, "llm connect") {
		t.Fatalf("answerNotice = %q; want ⚠ notice with hint", got)
	}
}
