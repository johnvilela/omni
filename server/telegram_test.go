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
func fakeTelegram(t *testing.T, sent chan<- map[string]any) *httptest.Server {
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/getMe", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"username":"omni_test_bot"}}`)
	})
	mux.HandleFunc("/botTOKEN/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":7,"message":{"chat":{"id":42},"text":"hello"}}]}`)
			return
		}
		if r.URL.Query().Get("offset") != "8" {
			t.Errorf("getUpdates offset = %q, want 8", r.URL.Query().Get("offset"))
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	})
	mux.HandleFunc("/botTOKEN/sendMessage", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		sent <- body
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	})
	return httptest.NewServer(mux)
}

func TestTelegramGetMe(t *testing.T) {
	srv := fakeTelegram(t, nil)
	defer srv.Close()

	tg := NewTelegram(srv.URL, "TOKEN")
	name, err := tg.GetMe(context.Background())
	if err != nil || name != "omni_test_bot" {
		t.Fatalf("GetMe = %q, %v; want omni_test_bot, nil", name, err)
	}
}

func TestTelegramPollRepliesReversed(t *testing.T) {
	sent := make(chan map[string]any, 1)
	srv := fakeTelegram(t, sent)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	tg := NewTelegram(srv.URL, "TOKEN")
	go func() { tg.Poll(ctx); close(done) }()

	select {
	case body := <-sent:
		if body["chat_id"] != float64(42) || body["text"] != "olleh" {
			t.Fatalf("sendMessage body = %v, want chat_id 42, text olleh", body)
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
