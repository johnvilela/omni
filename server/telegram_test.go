package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTelegram serves getMe, one update on the first getUpdates, then empties.
// sendChatAction bodies go to actions when non-nil.
func fakeTelegram(t *testing.T, sent chan<- map[string]any, actions chan<- map[string]any) *httptest.Server {
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/getMe", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"username":"omni_test_bot"}}`)
	})
	mux.HandleFunc("/botTOKEN/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":7,"message":{"chat":{"id":42},"from":{"id":99},"text":"hello"}}]}`)
			return
		}
		if r.URL.Query().Get("offset") != "8" {
			t.Errorf("getUpdates offset = %q, want 8", r.URL.Query().Get("offset"))
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	})
	mux.HandleFunc("/botTOKEN/sendChatAction", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if actions != nil {
			actions <- body
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	mux.HandleFunc("/botTOKEN/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		sent <- body
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	})
	return httptest.NewServer(mux)
}

func TestTelegramPollSkipsEmptyReply(t *testing.T) {
	sent := make(chan map[string]any, 1)
	srv := fakeTelegram(t, sent, nil)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg := NewTelegram(srv.URL, "TOKEN")
	answered := make(chan struct{})
	tg.answer = func(context.Context, int64, string) string {
		close(answered)
		return "" // rate-limited senders get silence, not an empty sendMessage
	}
	go tg.Poll(ctx)

	<-answered
	select {
	case body := <-sent:
		t.Fatalf("sendMessage called with %v, want none for an empty reply", body)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestTelegramGetMe(t *testing.T) {
	srv := fakeTelegram(t, nil, nil)
	defer srv.Close()

	tg := NewTelegram(srv.URL, "TOKEN")
	name, err := tg.GetMe(context.Background())
	if err != nil || name != "omni_test_bot" {
		t.Fatalf("GetMe = %q, %v; want omni_test_bot, nil", name, err)
	}
}

func TestTelegramPollReplies(t *testing.T) {
	sent := make(chan map[string]any, 1)
	actions := make(chan map[string]any, 8)
	srv := fakeTelegram(t, sent, actions)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	tg := NewTelegram(srv.URL, "TOKEN")
	// blocking on the typing action proves the indicator fires while answering
	tg.answer = func(_ context.Context, from int64, text string) string {
		if from != 99 {
			t.Errorf("answer from = %d, want 99 (the sender id, not the chat)", from)
		}
		a := <-actions
		if a["chat_id"] != float64(42) || a["action"] != "typing" {
			t.Errorf("sendChatAction body = %v, want chat_id 42 typing", a)
		}
		return "echo:" + text
	}
	go func() { tg.Poll(ctx); close(done) }()

	select {
	case body := <-sent:
		if body["chat_id"] != float64(42) || body["text"] != "echo:hello" {
			t.Fatalf("sendMessage body = %v, want chat_id 42, text echo:hello", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sendMessage within 3s")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Poll did not stop after cancel")
	}
}
