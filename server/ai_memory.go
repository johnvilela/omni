package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	aiMemoryWorkspace = "omni"
	aiMemoryProject   = "assistant"
)

// aiMemoryClient talks to the one local ai-memory HTTP service. One process
// serves every read, write and capture; omni never starts a per-request MCP
// process, which matters on low-memory machines and rotational disks.
type aiMemoryClient struct {
	base   string
	http   *http.Client
	mu     sync.Mutex
	initOK bool
	seq    atomic.Int64
}

type aiCallContext struct{ sessionID, purpose string }
type aiCallContextKey struct{}

func withAICall(ctx context.Context, sessionID, purpose string) context.Context {
	return context.WithValue(ctx, aiCallContextKey{}, aiCallContext{sessionID: sessionID, purpose: purpose})
}

func aiCallFrom(ctx context.Context) aiCallContext {
	v, _ := ctx.Value(aiCallContextKey{}).(aiCallContext)
	if v.purpose == "" {
		v.purpose = "utility"
	}
	return v
}

func newAIMemoryClient(base string) *aiMemoryClient {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = "http://127.0.0.1:49374"
	}
	return &aiMemoryClient{base: base, http: &http.Client{Timeout: 3 * time.Second}}
}

func (m *aiMemoryClient) rpc(ctx context.Context, method string, params any, out any) error {
	id := m.seq.Add(1)
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ai-memory HTTP %s", resp.Status)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Error != nil {
		return fmt.Errorf("ai-memory: %s", envelope.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

func (m *aiMemoryClient) init(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.initOK {
		return nil
	}
	err := m.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"clientInfo":      map[string]string{"name": "omni", "version": version},
	}, nil)
	if err == nil {
		m.initOK = true
	}
	return err
}

func (m *aiMemoryClient) tool(ctx context.Context, name string, args map[string]any, out any) error {
	if err := m.init(ctx); err != nil {
		return err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := m.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &result); err != nil {
		return err
	}
	var text string
	for _, c := range result.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	if result.IsError {
		return fmt.Errorf("ai-memory: %s", strings.TrimSpace(text))
	}
	if out != nil && text != "" {
		return json.Unmarshal([]byte(text), out)
	}
	return nil
}

func (m *aiMemoryClient) recall(ctx context.Context, query string) string {
	query = memorySearchQuery(query)
	if query == "" {
		return ""
	}
	var result struct {
		Hits []struct {
			Path, Title, Snippet string
		} `json:"hits"`
		Global []struct {
			Path, Title, Snippet string
		} `json:"global_scope_hits"`
	}
	err := m.tool(ctx, "memory_query", map[string]any{
		"workspace": aiMemoryWorkspace, "project": aiMemoryProject,
		"query": query, "limit": 5,
	}, &result)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, h := range append(result.Hits, result.Global...) {
		fmt.Fprintf(&b, "- %s: %s\n", h.Title, stripMark(h.Snippet))
	}
	return strings.TrimSpace(b.String())
}

// memorySearchQuery converts arbitrary chat text into a bounded FTS5
// expression. User punctuation must not turn every memory lookup into a
// syntax error or allow an expensive operator-heavy query.
func memorySearchQuery(input string) string {
	terms := strings.FieldsFunc(input, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	quoted := make([]string, 0, 12)
	for _, term := range terms {
		if len([]rune(term)) < 2 {
			continue
		}
		quoted = append(quoted, `"`+term+`"`)
		if len(quoted) == 12 {
			break
		}
	}
	return strings.Join(quoted, " OR ")
}

func (m *aiMemoryClient) searchPaths(ctx context.Context, query, prefix string) []string {
	var result struct {
		Hits []struct {
			Path string `json:"path"`
		} `json:"hits"`
	}
	if err := m.tool(ctx, "memory_query", map[string]any{
		"workspace": aiMemoryWorkspace, "project": aiMemoryProject,
		"query": query, "limit": 100,
	}, &result); err != nil {
		return nil
	}
	var paths []string
	for _, h := range result.Hits {
		if strings.HasPrefix(h.Path, prefix) {
			paths = append(paths, h.Path)
		}
	}
	return paths
}

func (m *aiMemoryClient) readPage(ctx context.Context, path string) (string, error) {
	var page struct {
		Body string `json:"body"`
	}
	err := m.tool(ctx, "memory_read_page", map[string]any{
		"workspace": aiMemoryWorkspace, "project": aiMemoryProject, "path": path,
	}, &page)
	return page.Body, err
}

func (m *aiMemoryClient) writePage(ctx context.Context, path, body string, tags []string, pinned bool) error {
	return m.tool(ctx, "memory_write_page", map[string]any{
		"workspace": aiMemoryWorkspace, "project": aiMemoryProject,
		"path": path, "body": body, "tags": tags, "tier": "semantic", "pinned": pinned,
	}, nil)
}

func stripMark(s string) string {
	r := strings.NewReplacer("<mark>", "", "</mark>", "")
	return r.Replace(s)
}

// captureCall records every model request and response. Capture is best-effort:
// memory downtime must never make the assistant itself unavailable.
func (m *aiMemoryClient) captureCall(ctx context.Context, sessionID, purpose, prompt, reply string, callErr error) {
	if sessionID == "" {
		sessionID = fmt.Sprintf("omni-%d", time.Now().UnixNano())
	}
	m.capture(ctx, "user-prompt", purpose+".request", sessionID, map[string]any{"prompt": captureTail(prompt, 16*1024)})
	event := purpose + ".response"
	message := reply
	if callErr != nil {
		event, message = purpose+".error", callErr.Error()
	}
	m.capture(ctx, "notification", event, sessionID, map[string]any{"message": captureTail(message, 2*1024)})
}

func captureTail(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const marker = "[earlier context truncated]\n"
	start := len(s) - maxBytes + len(marker)
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return marker + s[start:]
}

func (m *aiMemoryClient) capture(ctx context.Context, event, sourceEvent, sessionID string, body any) {
	// Capture must not hold up a chat turn when memory is unhealthy, and a
	// model timeout should not cancel the error observation itself.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
	defer cancel()
	q := url.Values{
		"event": {event}, "agent": {"other"}, "extension": {"omni"},
		"source_event": {sourceEvent}, "session_id": {sessionID},
		"workspace": {aiMemoryWorkspace}, "project": {aiMemoryProject},
	}
	fields, _ := body.(map[string]any)
	fields["session_id"] = sessionID
	raw, err := json.Marshal(fields)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/hook?"+q.Encode(), bytes.NewReader(raw))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

var restartAIMemory = func() error {
	return exec.Command("systemctl", "--user", "restart", "ai-memory.service").Run()
}

func setMemoryRetention(days int) error {
	if err := saveConfigValue("memory_retention_days", days); err != nil {
		return err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	line := fmt.Sprintf("AI_MEMORY_DECAY__OBSERVATION_RETENTION_DAYS=%d\n", days)
	if err := os.WriteFile(filepath.Join(dir, "ai-memory.env"), []byte(line), 0o600); err != nil {
		return err
	}
	return restartAIMemory()
}

func memoryRetentionDays(cfg Config) int {
	if cfg.MemoryRetentionDays > 0 {
		return cfg.MemoryRetentionDays
	}
	return 30
}
