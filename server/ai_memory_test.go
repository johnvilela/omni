package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type memoryPages struct {
	mu   sync.Mutex
	data map[string]string
}

func (p *memoryPages) Get(path string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data[path]
}

func (p *memoryPages) Lookup(path string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	body, ok := p.data[path]
	return body, ok
}

func (p *memoryPages) Set(path, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.data[path] = body
}

func (p *memoryPages) Snapshot() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	copy := make(map[string]string, len(p.data))
	for path, body := range p.data {
		copy[path] = body
	}
	return copy
}

func newAIMemoryFake(t *testing.T) (*aiMemoryClient, *memoryPages) {
	t.Helper()
	pages := &memoryPages{data: map[string]string{}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hook" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["method"] == "initialize" {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{}})
			return
		}
		params := req["params"].(map[string]any)
		name := params["name"].(string)
		args := params["arguments"].(map[string]any)
		payload := map[string]any{}
		switch name {
		case "memory_read_page":
			path := args["path"].(string)
			body, ok := pages.Lookup(path)
			if !ok {
				json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "page not found"}}, "isError": true,
				}})
				return
			}
			payload = map[string]any{"path": path, "body": body}
		case "memory_write_page":
			pages.Set(args["path"].(string), args["body"].(string))
		case "memory_query":
			var hits []map[string]any
			for path, body := range pages.Snapshot() {
				if strings.Contains(body, "Omni core memory theme:") || strings.Contains(body, "Omni saved plan:") {
					hits = append(hits, map[string]any{"path": path, "title": strings.Split(body, "\n")[0], "snippet": body})
				}
			}
			payload = map[string]any{"hits": hits}
		}
		raw, _ := json.Marshal(payload)
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": string(raw)}},
		}})
	}))
	t.Cleanup(ts.Close)
	return newAIMemoryClient(ts.URL), pages
}

func TestAIMemoryQueryUsesDedicatedScope(t *testing.T) {
	var calls []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, req)
		if req["method"] == "initialize" {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{}})
			return
		}
		payload := `{"hits":[{"path":"notes/owner.md","title":"Owner","snippet":"Prefers dark mode"}]}`
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": payload}},
		}})
	}))
	defer ts.Close()

	m := newAIMemoryClient(ts.URL)
	got := m.recall(context.Background(), "display preferences")
	if !strings.Contains(got, "Owner") || !strings.Contains(got, "Prefers dark mode") {
		t.Fatalf("recall = %q", got)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want initialize + tools/call", len(calls))
	}
	params := calls[1]["params"].(map[string]any)
	args := params["arguments"].(map[string]any)
	if args["workspace"] != aiMemoryWorkspace || args["project"] != aiMemoryProject || args["query"] != `"display" OR "preferences"` {
		t.Fatalf("memory_query args = %#v", args)
	}
}

func TestRecallQueryEscapesUserPunctuation(t *testing.T) {
	if got := memorySearchQuery(`What's my son's name? "Theo"`); got != `"What" OR "my" OR "son" OR "name" OR "Theo"` {
		t.Fatalf("query = %q", got)
	}
}

func TestAIMemoryPageReadWrite(t *testing.T) {
	var wrote map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		if req["method"] == "initialize" {
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{}})
			return
		}
		params := req["params"].(map[string]any)
		name := params["name"].(string)
		if name == "memory_write_page" {
			wrote = params["arguments"].(map[string]any)
		}
		payload := `{}`
		if name == "memory_read_page" {
			payload = `{"path":"omni/core/general.md","body":"# Core memory: general\n\n- Likes Go"}`
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{
			"content": []map[string]string{{"type": "text", "text": payload}},
		}})
	}))
	defer ts.Close()

	m := newAIMemoryClient(ts.URL)
	body, err := m.readPage(context.Background(), "omni/core/general.md")
	if err != nil || !strings.Contains(body, "Likes Go") {
		t.Fatalf("readPage = %q, %v", body, err)
	}
	if err := m.writePage(context.Background(), "omni/core/general.md", "# Core memory: general\n\n- Likes Go", []string{"omni", "core"}, true); err != nil {
		t.Fatal(err)
	}
	if wrote["workspace"] != aiMemoryWorkspace || wrote["project"] != aiMemoryProject || wrote["pinned"] != true {
		t.Fatalf("write args = %#v", wrote)
	}
}

func TestAIMemoryCapturesEveryModelCall(t *testing.T) {
	var mu sync.Mutex
	var events []string
	var bodies []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, r.URL.Query().Get("source_event"))
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	m := newAIMemoryClient(ts.URL)
	m.captureCall(context.Background(), "session-1", "chat", "hello", "hi there", nil)

	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "chat.request,chat.response" {
		t.Fatalf("events = %v", events)
	}
	if bodies[0]["prompt"] != "hello" || bodies[1]["message"] != "hi there" {
		t.Fatalf("bodies = %#v", bodies)
	}
	if bodies[0]["session_id"] != "session-1" || bodies[1]["session_id"] != "session-1" {
		t.Fatalf("capture lost ai-memory session identity: %#v", bodies)
	}
}

func TestCaptureKeepsLatestUserTurnWithinHookLimit(t *testing.T) {
	var captured string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("event") == "user-prompt" {
			var body struct{ Prompt string }
			json.NewDecoder(r.Body).Decode(&body)
			captured = body.Prompt
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()
	m := newAIMemoryClient(ts.URL)
	m.captureCall(context.Background(), "session-1", "chat", strings.Repeat("x", 20000)+"\nlatest question", "reply", nil)
	if len(captured) > 16*1024 || !strings.HasSuffix(captured, "latest question") {
		t.Fatalf("captured prompt length %d, suffix %q", len(captured), captured[len(captured)-32:])
	}
}

func TestAnswerCapturesBareModelCall(t *testing.T) {
	var mu sync.Mutex
	var events []string
	memory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.URL.Query().Get("source_event"))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer memory.Close()

	srv, _ := newLLMTestServer(t)
	srv.aiMemory = newAIMemoryClient(memory.URL)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	if _, code, err := srv.ConnectLLM(context.Background(), "openai", ""); code != 200 || err != nil {
		t.Fatalf("connect = %d, %v", code, err)
	}
	ctx := withAICall(context.Background(), "chat-session", "chat")
	if got, err := srv.Answer(ctx, "ping"); err != nil || got != "pong" {
		t.Fatalf("Answer = %q, %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "chat.request,chat.response" {
		t.Fatalf("events = %v", events)
	}
}

func TestRunAgentModelCapturesRequestAndResponse(t *testing.T) {
	old := runClaudeAgentCall
	runClaudeAgentCall = func(context.Context, string, string) (string, string, callUsage, error) {
		return "agent reply", "vendor-2", callUsage{in: 3, out: 2}, nil
	}
	t.Cleanup(func() { runClaudeAgentCall = old })

	var mu sync.Mutex
	var events []string
	memory := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		events = append(events, r.URL.Query().Get("source_event"))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer memory.Close()

	srv, _ := newTestServer(t)
	srv.aiMemory = newAIMemoryClient(memory.URL)
	reply, vendorID, _, err := srv.runAgentModel(context.Background(), "session-1", "agent", "claude", "vendor-1", "do it")
	if err != nil || reply != "agent reply" || vendorID != "vendor-2" {
		t.Fatalf("runAgentModel = %q, %q, %v", reply, vendorID, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(events, ",") != "agent.request,agent.response" {
		t.Fatalf("events = %v", events)
	}
}

func TestMemoryRetentionCommand(t *testing.T) {
	dir := t.TempDir()
	oldRestart := restartAIMemory
	restarted := false
	restartAIMemory = func() error { restarted = true; return nil }
	t.Cleanup(func() { restartAIMemory = oldRestart })

	srv, _ := newTestServer(t)
	t.Setenv("XDG_CONFIG_HOME", dir)
	r := srv.handleMessage(context.Background(), "/memory_retention 45d")
	if !strings.Contains(r.Text, "45 days") || !restarted {
		t.Fatalf("reply = %q, restarted = %v", r.Text, restarted)
	}
	if got := readConfig().MemoryRetentionDays; got != 45 {
		t.Fatalf("memory_retention_days = %d", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, app, "ai-memory.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "AI_MEMORY_DECAY__OBSERVATION_RETENTION_DAYS=45\n" {
		t.Fatalf("env = %q", raw)
	}

	if bad := srv.handleMessage(context.Background(), "/memory_retention forever"); !strings.Contains(bad.Text, "usage:") {
		t.Fatalf("bad duration reply = %q", bad.Text)
	}
}

func TestCoreMemoryUsesAIMemoryPages(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.aiMemory, _ = newAIMemoryFake(t)
	ctx := context.Background()
	if err := srv.appendCore(ctx, "family", "Owner's son is named Theo"); err != nil {
		t.Fatal(err)
	}
	if got := srv.readCoreTheme(ctx, "family"); !strings.Contains(got, "Theo") {
		t.Fatalf("theme = %q", got)
	}
	prompt := srv.corePrompt(ctx, Session{Themes: "family"})
	if !strings.Contains(prompt, "Theo") || !strings.Contains(prompt, "family") {
		t.Fatalf("core prompt = %q", prompt)
	}
}

func TestPlanSaveUsesAIMemoryPage(t *testing.T) {
	srv, _ := newTestServer(t)
	memory, pages := newAIMemoryFake(t)
	srv.aiMemory = memory
	ctx := context.Background()
	got := srv.runTool(ctx, "", "plan_save", `{"title":"Ship feature","tags":["long"],"body":"## Goal\nShip it"}`)
	if !strings.Contains(got, "plan saved") {
		t.Fatalf("save reply = %q", got)
	}
	page := pages.Get("omni/plans/ship-feature.md")
	if !strings.Contains(page, "Status: active") || !strings.Contains(page, "#long") {
		t.Fatalf("plan page = %q", page)
	}
	if prompt := srv.plansPrompt(ctx); !strings.Contains(prompt, "ship-feature · active · #long") {
		t.Fatalf("plans prompt = %q", prompt)
	}
}
