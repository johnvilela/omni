package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// newLLMTestServer isolates every llm credential source (creds files under a
// fake HOME, env keys, PATH for the claude binary) and fakes all three
// provider APIs: key GOOD validates, anything else is 401.
func newLLMTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	srv, store := newTestServer(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("PATH", t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") == "Bearer GOOD":
			fmt.Fprint(w, `{"data":[{"id":"gpt-test"},{"id":"whisper-1"},{"id":"o3-mini"}]}`)
		case r.Header.Get("x-api-key") == "GOOD":
			fmt.Fprint(w, `{"data":[{"id":"claude-test"}]}`)
		default:
			w.WriteHeader(401)
		}
	})
	mux.HandleFunc("/v1beta/models", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") == "GOOD" {
			fmt.Fprint(w, `{"models":[{"name":"models/gemini-test","supportedGenerationMethods":["generateContent"]},{"name":"models/embedding-test","supportedGenerationMethods":["embedContent"]}]}`)
			return
		}
		w.WriteHeader(401)
	})
	// chat endpoints for Answer: canned "pong" when the key is GOOD
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer GOOD" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"pong"}}]}`)
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "GOOD" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"content":[{"type":"text","text":"pong"}]}`)
	})
	mux.HandleFunc("/v1beta/models/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "GOOD" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}`)
	})
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	for _, env := range []string{"OMNI_OPENAI_API", "OMNI_CLAUDE_API", "OMNI_GEMINI_API"} {
		t.Setenv(env, fake.URL)
	}
	return srv, store
}

func writeCreds(t *testing.T, rel, content string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLLMListDisconnected(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	code, _, list := doJSON(t, srv.Handler(), "GET", "/llm", "")
	if code != 200 || len(list) != 3 {
		t.Fatalf("GET /llm = %d, %v; want 200 with 3 providers", code, list)
	}
	for i, name := range llmProviders {
		if list[i]["name"] != name || list[i]["connected"] != false {
			t.Fatalf("provider %d = %v, want %s disconnected", i, list[i], name)
		}
	}
}

func TestLLMUnknownProvider(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	h := srv.Handler()
	if code, obj, _ := doJSON(t, h, "GET", "/llm/cohere", ""); code != 404 || obj["error"] != "unknown_provider" {
		t.Fatalf("GET /llm/cohere = %d, %v; want 404 unknown_provider", code, obj)
	}
	if code, obj, _ := doJSON(t, h, "POST", "/llm/cohere/connect", "{}"); code != 404 || obj["error"] != "unknown_provider" {
		t.Fatalf("POST /llm/cohere/connect = %d, %v; want 404 unknown_provider", code, obj)
	}
}

func TestLLMConnectWithoutKey(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	code, obj, _ := doJSON(t, srv.Handler(), "POST", "/llm/openai/connect", "{}")
	if code != 400 || obj["error"] != "key_required" {
		t.Fatalf("connect without key = %d, %v; want 400 key_required", code, obj)
	}
}

func TestLLMConnectBadKey(t *testing.T) {
	srv, store := newLLMTestServer(t)
	code, obj, _ := doJSON(t, srv.Handler(), "POST", "/llm/openai/connect", `{"key":"BAD"}`)
	if code != 401 {
		t.Fatalf("connect with bad key = %d, %v; want 401", code, obj)
	}
	if ok, _ := store.Connected("llm:openai"); ok {
		t.Fatal("store marked connected after a bad key")
	}
}

func TestLLMConnectHappyPath(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	h := srv.Handler()

	code, obj, _ := doJSON(t, h, "POST", "/llm/openai/connect", "{}")
	if code != 200 || obj["connected"] != true || obj["source"] != "api_key" {
		t.Fatalf("connect = %d, %v; want 200 connected via api_key", code, obj)
	}

	_, obj, _ = doJSON(t, h, "GET", "/llm/openai", "")
	if obj["connected"] != true || obj["source"] != "api_key" {
		t.Fatalf("detail after connect = %v", obj)
	}

	if ok, _ := store.Connected("llm:openai"); !ok {
		t.Fatal("store not marked connected")
	}
}

func TestLLMConnectOAuthFile(t *testing.T) {
	cases := []struct{ provider, rel, content string }{
		{"openai", filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x","tokens":null}`},
		{"claude", filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`},
		{"gemini", filepath.Join(".gemini", "oauth_creds.json"), `{"access_token":"tok","refresh_token":"ref"}`},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv, _ := newLLMTestServer(t)
			writeCreds(t, c.rel, c.content)
			code, obj, _ := doJSON(t, srv.Handler(), "POST", "/llm/"+c.provider+"/connect", "{}")
			if code != 200 || obj["connected"] != true || obj["source"] != "oauth" {
				t.Fatalf("connect via creds file = %d, %v; want 200 connected via oauth", code, obj)
			}
		})
	}
}

func TestLLMClaudeBinaryFallback(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	code, obj, _ := doJSON(t, srv.Handler(), "POST", "/llm/claude/connect", "{}")
	if code != 200 || obj["connected"] != true || obj["source"] != "claude-code" {
		t.Fatalf("connect via claude binary = %d, %v; want 200 connected via claude-code", code, obj)
	}
}

func TestLLMDisconnectsWhenCredsGone(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	t.Setenv("GEMINI_API_KEY", "GOOD")
	h := srv.Handler()

	if code, _, _ := doJSON(t, h, "POST", "/llm/gemini/connect", "{}"); code != 200 {
		t.Fatalf("connect = %d, want 200", code)
	}
	t.Setenv("GEMINI_API_KEY", "")
	_, obj, _ := doJSON(t, h, "GET", "/llm/gemini", "")
	if obj["connected"] != false {
		t.Fatalf("detail after creds gone = %v; want disconnected", obj)
	}
}

func TestLLMImpliedDefault(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	h := srv.Handler()

	// the only connected llm is the implied default
	code, obj, _ := doJSON(t, h, "POST", "/llm/openai/connect", "{}")
	if code != 200 || obj["default"] != true {
		t.Fatalf("connect only llm = %d, %v; want implied default", code, obj)
	}
	_, obj, _ = doJSON(t, h, "GET", "/llm/openai", "")
	if obj["default"] != true {
		t.Fatalf("detail = %v; want implied default", obj)
	}

	// an explicit default_llm always wins over the implied one
	dir, _ := os.UserConfigDir()
	cfgPath := filepath.Join(dir, app, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("default_llm: claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, list := doJSON(t, h, "GET", "/llm", "")
	for _, l := range list {
		if (l["default"] == true) != (l["name"] == "claude") {
			t.Fatalf("explicit default must win over implied: %v", list)
		}
	}

	// two connected and no explicit default: nobody is default
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "GOOD")
	if code, _, _ := doJSON(t, h, "POST", "/llm/gemini/connect", "{}"); code != 200 {
		t.Fatal("connect gemini failed")
	}
	_, _, list = doJSON(t, h, "GET", "/llm", "")
	for _, l := range list {
		if l["default"] == true {
			t.Fatalf("two connected, no explicit default: %v", list)
		}
	}
}

func TestLLMDefault(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("default_llm: claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	_, _, list := doJSON(t, h, "GET", "/llm", "")
	for _, l := range list {
		want := l["name"] == "claude"
		if (l["default"] == true) != want {
			t.Fatalf("default flags wrong: %v", list)
		}
	}

	_, obj, _ := doJSON(t, h, "GET", "/llm/openai", "")
	if _, ok := obj["default"]; ok {
		t.Fatalf("non-default provider should omit the default field: %v", obj)
	}
}

func TestLLMStatusModelEffort(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"),
		[]byte("claude_model: claude-test\nclaude_effort: high\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()

	_, obj, _ := doJSON(t, h, "GET", "/llm/claude", "")
	if obj["model"] != "claude-test" || obj["effort"] != "high" {
		t.Fatalf("claude status = %v; want model and effort from config", obj)
	}
	_, obj, _ = doJSON(t, h, "GET", "/llm/openai", "")
	if _, ok := obj["model"]; ok {
		t.Fatalf("openai status = %v; want model omitted when unset", obj)
	}
	if _, ok := obj["effort"]; ok {
		t.Fatalf("openai status = %v; want effort omitted when unset", obj)
	}
}

// getModels fetches GET /llm/{provider}/models and decodes the string array.
func getModels(t *testing.T, h http.Handler, provider string) (int, []string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/llm/"+provider+"/models", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var ms []string
	json.Unmarshal(w.Body.Bytes(), &ms)
	return w.Code, ms
}

func TestLLMModelsEndpoint(t *testing.T) {
	srv, _ := newLLMTestServer(t)
	h := srv.Handler()

	if code, obj, _ := doJSON(t, h, "GET", "/llm/cohere/models", ""); code != 404 || obj["error"] != "unknown_provider" {
		t.Fatalf("GET /llm/cohere/models = %d, %v; want 404 unknown_provider", code, obj)
	}

	// no credentials at all: the curated fallback list
	if code, ms := getModels(t, h, "openai"); code != 200 || !slices.Equal(ms, llmFallbackModels["openai"]) {
		t.Fatalf("no creds = %d, %v; want fallback %v", code, ms, llmFallbackModels["openai"])
	}

	// api keys: live lists, noise filtered, gemini prefix stripped
	t.Setenv("OPENAI_API_KEY", "GOOD")
	if _, ms := getModels(t, h, "openai"); !slices.Equal(ms, []string{"gpt-test", "o3-mini"}) {
		t.Fatalf("openai live = %v; want whisper filtered out", ms)
	}
	t.Setenv("ANTHROPIC_API_KEY", "GOOD")
	if _, ms := getModels(t, h, "claude"); !slices.Equal(ms, []string{"claude-test"}) {
		t.Fatalf("claude live = %v", ms)
	}
	t.Setenv("GEMINI_API_KEY", "GOOD")
	if _, ms := getModels(t, h, "gemini"); !slices.Equal(ms, []string{"gemini-test"}) {
		t.Fatalf("gemini live = %v; want models/ stripped and embeddings filtered", ms)
	}

	// a bad key (fetch 401s): fallback, not an error
	t.Setenv("OPENAI_API_KEY", "BAD")
	if code, ms := getModels(t, h, "openai"); code != 200 || !slices.Equal(ms, llmFallbackModels["openai"]) {
		t.Fatalf("bad key = %d, %v; want fallback", code, ms)
	}

	// oauth creds shadow the key: openai answers go through codex, which
	// (on ChatGPT logins) accepts only its own model set — not the api list
	t.Setenv("OPENAI_API_KEY", "GOOD")
	writeCreds(t, filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`)
	_, ms := getModels(t, h, "openai")
	if !slices.Equal(ms, llmCodexModels) {
		t.Fatalf("openai oauth = %v; want the codex list %v", ms, llmCodexModels)
	}
	if slices.Contains(ms, "gpt-4.1") {
		t.Fatalf("openai oauth = %v; gpt-4.1 is api-only, codex rejects it", ms)
	}

	// claude oauth: the vendor CLI takes the same model ids, plain fallback
	writeCreds(t, filepath.Join(".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"tok"}}`)
	if _, ms := getModels(t, h, "claude"); !slices.Equal(ms, llmFallbackModels["claude"]) {
		t.Fatalf("claude oauth = %v; want fallback", ms)
	}
}

// TestResolveLLM climbs the precedence ladder bottom-up, asserting each new
// source shadows the one below it.
func TestResolveLLM(t *testing.T) {
	newLLMTestServer(t)

	if s, _ := resolveLLM("openai", ""); s != "" {
		t.Fatalf("nothing set: source = %q, want none", s)
	}

	// config.yaml
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("openai_key: CFG\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, k := resolveLLM("openai", ""); s != "api_key" || k != "CFG" {
		t.Fatalf("config: = %q %q, want api_key CFG", s, k)
	}

	// env beats config
	t.Setenv("OPENAI_API_KEY", "ENV")
	if s, k := resolveLLM("openai", ""); s != "api_key" || k != "ENV" {
		t.Fatalf("env: = %q %q, want api_key ENV", s, k)
	}

	// vendor creds file beats env
	writeCreds(t, filepath.Join(".codex", "auth.json"), `{"OPENAI_API_KEY":"sk-x"}`)
	if s, _ := resolveLLM("openai", ""); s != "oauth" {
		t.Fatalf("creds file: source = %q, want oauth", s)
	}

	// explicit request key beats everything
	if s, k := resolveLLM("openai", "REQ"); s != "api_key" || k != "REQ" {
		t.Fatalf("request: = %q %q, want api_key REQ", s, k)
	}
}
