package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakePinAPI records every bot-API call as {"method": name, ...body} on calls
// and replies ok with message_id 100 (valid for every method the pin uses).
func fakePinAPI(t *testing.T, calls chan<- map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		json.NewDecoder(r.Body).Decode(&body)
		body["method"] = strings.TrimPrefix(r.URL.Path, "/botTOKEN/")
		calls <- body
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":100}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newPinServer is a test server with one approved pairing (chat 42) and a
// recording telegram attached directly (no poller).
func newPinServer(t *testing.T) (*Server, *Store, chan map[string]any) {
	t.Helper()
	srv, store := newTestServer(t)
	if err := store.AddPairing("telegram", "42", "CODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePairing("telegram", "CODE"); err != nil {
		t.Fatal(err)
	}
	calls := make(chan map[string]any, 32)
	fake := fakePinAPI(t, calls)
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	return srv, store, calls
}

func nextCall(t *testing.T, calls <-chan map[string]any, method string) map[string]any {
	t.Helper()
	select {
	case c := <-calls:
		if c["method"] != method {
			t.Fatalf("call = %v, want method %s", c, method)
		}
		return c
	case <-time.After(3 * time.Second):
		t.Fatalf("no %s call within 3s", method)
		return nil
	}
}

func noCall(t *testing.T, calls <-chan map[string]any) {
	t.Helper()
	select {
	case c := <-calls:
		t.Fatalf("unexpected call %v", c)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPinToggle(t *testing.T) {
	srv, store, calls := newPinServer(t)

	if r := srv.handlePin(context.Background(), ""); r.Text != "" {
		t.Fatalf("pin reply = %q, want silence", r.Text)
	}
	sent := nextCall(t, calls, "sendMessage")
	if sent["chat_id"] != float64(42) || sent["text"] != "▶ — · 0 running · 0 unread" {
		t.Fatalf("sendMessage body = %v", sent)
	}
	pinned := nextCall(t, calls, "pinChatMessage")
	if pinned["message_id"] != float64(100) || pinned["disable_notification"] != true {
		t.Fatalf("pinChatMessage body = %v", pinned)
	}
	pins, err := store.Pins()
	if err != nil || len(pins) != 1 || pins[0] != (Pin{ChatID: 42, MessageID: 100, Mode: "clean"}) {
		t.Fatalf("Pins = %v, %v; want one {42 100 clean}", pins, err)
	}

	if r := srv.handlePin(context.Background(), ""); r.Text != "dashboard unpinned" {
		t.Fatalf("unpin reply = %q", r.Text)
	}
	nextCall(t, calls, "unpinChatMessage")
	del := nextCall(t, calls, "deleteMessage")
	if del["message_id"] != float64(100) {
		t.Fatalf("deleteMessage body = %v", del)
	}
	if pins, _ := store.Pins(); len(pins) != 0 {
		t.Fatalf("Pins after unpin = %v, want empty", pins)
	}
}

func TestPinRefreshEditsInPlace(t *testing.T) {
	srv, store, calls := newPinServer(t)
	srv.handlePin(context.Background(), "")
	nextCall(t, calls, "sendMessage")
	nextCall(t, calls, "pinChatMessage")

	srv.qmu.Lock()
	srv.queues = map[string]*sessionQueue{"x": {}}
	srv.qmu.Unlock()
	if err := store.AddSession("a", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendSessionUnread("a", "answer"); err != nil {
		t.Fatal(err)
	}

	srv.refreshPin()
	edit := nextCall(t, calls, "editMessageText")
	if edit["message_id"] != float64(100) || edit["text"] != "▶ a · 1 running · 1 unread" {
		t.Fatalf("editMessageText body = %v", edit)
	}
}

func TestPinRefreshDedupe(t *testing.T) {
	srv, _, calls := newPinServer(t)
	srv.handlePin(context.Background(), "")
	nextCall(t, calls, "sendMessage")
	nextCall(t, calls, "pinChatMessage")

	srv.refreshPin() // state unchanged since create — no edit
	noCall(t, calls)
}

func TestPinFullMode(t *testing.T) {
	srv, store, calls := newPinServer(t)
	for _, id := range []string{"a", "b"} {
		if err := store.AddSession(id, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	store.SetSessionName("a", "alpha")
	store.SetSessionName("b", "beta")
	store.AppendSessionUnread("a", "answer")
	store.SetActiveSession("b")

	srv.handlePin(context.Background(), "full")
	sent := nextCall(t, calls, "sendMessage")
	want := "▶ beta · 0 running · 1 unread\n\n✉ alpha\n▶ beta" // unread floats first
	if sent["text"] != want {
		t.Fatalf("full dashboard = %q, want %q", sent["text"], want)
	}
	nextCall(t, calls, "pinChatMessage")

	if r := srv.handlePin(context.Background(), "clean"); r.Text != "dashboard: clean" {
		t.Fatalf("mode switch reply = %q", r.Text)
	}
	edit := nextCall(t, calls, "editMessageText") // async refresh after mode switch
	if edit["text"] != "▶ beta · 0 running · 1 unread" {
		t.Fatalf("clean dashboard = %q", edit["text"])
	}
}

func TestPinRestartReattach(t *testing.T) {
	srv, store, calls := newPinServer(t)
	// a pin persisted by a previous process: no pinLast cache, empty queues
	if err := store.SetPin(42, 100, "clean"); err != nil {
		t.Fatal(err)
	}
	srv.refreshPin()
	edit := nextCall(t, calls, "editMessageText")
	if edit["message_id"] != float64(100) || edit["text"] != "▶ — · 0 running · 0 unread" {
		t.Fatalf("editMessageText body = %v", edit)
	}
}

func TestPinUnpinnedRefreshIsNoop(t *testing.T) {
	srv, _, calls := newPinServer(t)
	srv.refreshPin()
	noCall(t, calls)
}

func TestPinNotModifiedIgnored(t *testing.T) {
	srv, store := newTestServer(t)
	store.AddPairing("telegram", "42", "CODE")
	store.ApprovePairing("telegram", "CODE")
	calls := make(chan map[string]any, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/", func(w http.ResponseWriter, r *http.Request) {
		calls <- map[string]any{"method": strings.TrimPrefix(r.URL.Path, "/botTOKEN/")}
		fmt.Fprint(w, `{"ok":false,"description":"Bad Request: message is not modified"}`)
	})
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	if err := store.SetPin(42, 100, "clean"); err != nil {
		t.Fatal(err)
	}

	srv.refreshPin()
	nextCall(t, calls, "editMessageText")
	srv.refreshPin() // not-modified counted as success: cache set, no retry
	noCall(t, calls)
}
