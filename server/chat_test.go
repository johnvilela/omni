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
	"sync"
	"testing"
	"time"
)

func TestComposePromptDegenerate(t *testing.T) {
	// no memory + no history must behave byte-for-byte like the old
	// single-turn bot: the raw text, nothing else.
	prompt, dropped := composePrompt("", "", nil, "ping", 8000)
	if prompt != "ping" || len(dropped) != 0 {
		t.Fatalf("composePrompt(bare) = %q, %v; want raw text, no dropped", prompt, dropped)
	}
}

func TestComposePromptSections(t *testing.T) {
	history := []Message{
		{ID: 1, Role: "user", Content: "ping"},
		{ID: 2, Role: "assistant", Content: "pong"},
	}
	prompt, dropped := composePrompt("", "owner likes go", history, "again?", 8000)
	want := "Long-term memory about the user:\nowner likes go\n\n" +
		"Conversation so far:\nuser: ping\nassistant: pong\n\n" +
		"New user message (answer this):\nagain?"
	if prompt != want {
		t.Fatalf("composePrompt = %q\nwant %q", prompt, want)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped = %v; want none", dropped)
	}
}

func TestComposePromptBudget(t *testing.T) {
	old := Message{ID: 1, Role: "user", Content: strings.Repeat("a", 40)}        // 10 tokens
	newer := Message{ID: 2, Role: "assistant", Content: strings.Repeat("b", 40)} // 10 tokens
	prompt, dropped := composePrompt("", "", []Message{old, newer}, "hi", 15)
	if strings.Contains(prompt, old.Content) || !strings.Contains(prompt, newer.Content) {
		t.Fatalf("budget walk kept the wrong turns: %q", prompt)
	}
	if len(dropped) != 1 || dropped[0].ID != 1 {
		t.Fatalf("dropped = %v; want just the old turn", dropped)
	}
}

func TestComposePromptOversizedMessage(t *testing.T) {
	history := []Message{{ID: 1, Role: "user", Content: "ping"}}
	text := strings.Repeat("x", 100) // 25 tokens, over the whole budget
	prompt, dropped := composePrompt("", "", history, text, 10)
	if !strings.Contains(prompt, text) {
		t.Fatal("oversized message must still be sent")
	}
	if strings.Contains(prompt, "Conversation so far") || len(dropped) != 1 {
		t.Fatalf("oversized message must drop all history: %q, dropped %v", prompt, dropped)
	}
}

func TestComposePromptMemoryCountsAgainstBudget(t *testing.T) {
	memory := strings.Repeat("m", 40) // 10 tokens
	history := []Message{{ID: 1, Role: "user", Content: strings.Repeat("a", 40)}}
	prompt, dropped := composePrompt("", memory, history, "hi", 12)
	if !strings.Contains(prompt, memory) {
		t.Fatal("memory section must always be included whole")
	}
	if strings.Contains(prompt, history[0].Content) || len(dropped) != 1 {
		t.Fatalf("memory must count against the budget: %q, dropped %v", prompt, dropped)
	}
}

// TestComposePromptPersona: the persona rides whole atop every prompt and
// counts against the budget like memory.
func TestComposePromptPersona(t *testing.T) {
	history := []Message{{ID: 1, Role: "user", Content: "ping"}}
	prompt, _ := composePrompt("be brief", "owner likes go", history, "hi", 8000)
	if !strings.HasPrefix(prompt, "be brief\n\n") {
		t.Fatalf("persona must lead the prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "Long-term memory about the user:\nowner likes go") {
		t.Fatalf("memory section lost: %q", prompt)
	}

	// budget: a 10-token persona at budget 12 evicts the 10-token history turn
	persona := strings.Repeat("p", 40)
	history = []Message{{ID: 1, Role: "user", Content: strings.Repeat("a", 40)}}
	prompt, dropped := composePrompt(persona, "", history, "hi", 12)
	if !strings.Contains(prompt, persona) {
		t.Fatal("persona must always be included whole")
	}
	if strings.Contains(prompt, history[0].Content) || len(dropped) != 1 {
		t.Fatalf("persona must count against the budget: %q, dropped %v", prompt, dropped)
	}

	// persona alone (no memory, no history) still frames the message
	prompt, _ = composePrompt("be brief", "", nil, "hi", 8000)
	if !strings.HasPrefix(prompt, "be brief") || !strings.Contains(prompt, "New user message (answer this):\nhi") {
		t.Fatalf("persona-only prompt = %q", prompt)
	}
}

// promptRecorder fakes the openai chat endpoint, recording every prompt it
// receives (the naming call interleaves with chat calls, so tests search all
// recorded prompts instead of assuming an order).
type promptRecorder struct {
	mu      sync.Mutex
	prompts []string
	reply   string
}

func (r *promptRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	if len(body.Messages) > 0 {
		r.prompts = append(r.prompts, body.Messages[0].Content)
	}
	reply := r.reply
	r.mu.Unlock()
	fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, reply)
}

func (r *promptRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prompts...)
}

// newChatTestServer connects openai via key, then re-points the chat API at
// a recording fake so tests can inspect the exact prompts sent.
func newChatTestServer(t *testing.T) (*Server, *Store, *promptRecorder) {
	t.Helper()
	srv, store := newLLMTestServer(t)
	t.Setenv("OPENAI_API_KEY", "GOOD")
	if _, code, err := srv.ConnectLLM(context.Background(), "openai", ""); code != 200 {
		t.Fatalf("connect openai = %d, %v", code, err)
	}
	rec := &promptRecorder{reply: "pong"}
	fake := httptest.NewServer(rec)
	t.Cleanup(fake.Close)
	t.Setenv("OMNI_OPENAI_API", fake.URL)
	return srv, store, rec
}

func TestChatAnswerCreatesSession(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	got, err := srv.ChatAnswer(context.Background(), "ping")
	if err != nil || got != "pong" {
		t.Fatalf("ChatAnswer = %q, %v; want pong", got, err)
	}
	sess, ok, err := store.ActiveSession()
	if err != nil || !ok || len(sess.ID) != 36 {
		t.Fatalf("ActiveSession = %+v, ok %v, %v; want auto-created uuid session", sess, ok, err)
	}
	ms, _ := store.Messages(sess.ID)
	if len(ms) != 2 || ms[0].Role != "user" || ms[0].Content != "ping" || ms[1].Role != "assistant" || ms[1].Content != "pong" {
		t.Fatalf("Messages = %+v; want the exchange", ms)
	}
	// first message of a fresh install is the bare text, exactly like today
	if prompts := rec.all(); len(prompts) == 0 || prompts[0] != "ping" {
		t.Fatalf("first prompt = %v; want bare ping", prompts)
	}
}

// TestChatAnswerInjectsPersona: the config-folder AGENTS.md leads every
// composed prompt — chat is stateless, so "every turn" is what makes the
// model never forget it.
func TestChatAnswerInjectsPersona(t *testing.T) {
	srv, _, rec := newChatTestServer(t)
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "AGENTS.md"), []byte("SPEAK-LIKE-A-PIRATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChatAnswer(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChatAnswer(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, p := range rec.all() {
		if strings.HasPrefix(p, "SPEAK-LIKE-A-PIRATE") {
			seen++
		}
	}
	if seen < 2 {
		t.Fatalf("persona led %d prompts; want both chat turns (all: %q)", seen, rec.all())
	}
}

func TestChatAnswerInjectsHistory(t *testing.T) {
	srv, _, rec := newChatTestServer(t)
	if _, err := srv.ChatAnswer(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChatAnswer(context.Background(), "what did I say?"); err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.all() {
		if strings.Contains(p, "user: ping\nassistant: pong") &&
			strings.Contains(p, "New user message (answer this):\nwhat did I say?") {
			return
		}
	}
	t.Fatalf("no prompt carried the history: %q", rec.all())
}

func TestChatAnswerBudget(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("token_budget: 20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", 100) // 25 est. tokens: never fits a 20 budget
	if _, err := srv.ChatAnswer(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.ChatAnswer(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	var second string
	for _, p := range rec.all() {
		if strings.Contains(p, "New user message (answer this):\nhi") {
			second = p
		}
	}
	if second == "" {
		t.Fatalf("second chat prompt not recorded: %q", rec.all())
	}
	if strings.Contains(second, long) || !strings.Contains(second, "assistant: pong") {
		t.Fatalf("budget walk kept the wrong turns: %q", second)
	}
	// no memoria wiki → compaction never fires, watermark untouched
	if sess, _, _ := store.ActiveSession(); sess.ConsolidatedUntil != 0 {
		t.Fatalf("consolidated_until = %d without memoria; want 0", sess.ConsolidatedUntil)
	}
}

func TestChatAnswerNaming(t *testing.T) {
	srv, store, _ := newChatTestServer(t)
	if _, err := srv.ChatAnswer(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess, _, _ := store.ActiveSession(); sess.Name == "pong" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	sess, _, _ := store.ActiveSession()
	t.Fatalf("session name = %q; want llm title", sess.Name)
}

func TestAnswerNoticeEmptyReply(t *testing.T) {
	srv, _, rec := newChatTestServer(t)
	rec.reply = ""
	if got := srv.answerNotice(context.Background(), "hi"); got != "(empty reply)" {
		t.Fatalf("answerNotice(empty) = %q; want (empty reply)", got)
	}
}

func TestTokenBudget(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := tokenBudget(); got != 8000 {
		t.Fatalf("tokenBudget(no config) = %d; want 8000", got)
	}
	dir, _ := os.UserConfigDir()
	if err := os.MkdirAll(filepath.Join(dir, app), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("token_budget: 123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tokenBudget(); got != 123 {
		t.Fatalf("tokenBudget(config) = %d; want 123", got)
	}
}
