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

// fakeClearAPI records every bot-API call as {"method": name, ...body} and
// replies ok, except methods listed in fail, which get a telegram error.
func fakeClearAPI(t *testing.T, calls chan<- map[string]any, fail ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/", func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		json.NewDecoder(r.Body).Decode(&body)
		method := strings.TrimPrefix(r.URL.Path, "/botTOKEN/")
		body["method"] = method
		calls <- body
		for _, f := range fail {
			if f == method {
				fmt.Fprint(w, `{"ok":false,"description":"Bad Request: message can't be deleted"}`)
				return
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newClearServer is a test server with one approved pairing (chat 42), a
// recording telegram attached directly, and tracked message ids 1..n.
func newClearServer(t *testing.T, n int, fail ...string) (*Server, *Store, chan map[string]any) {
	t.Helper()
	srv, store := newTestServer(t)
	if err := store.AddPairing("telegram", "42", "CODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePairing("telegram", "CODE"); err != nil {
		t.Fatal(err)
	}
	calls := make(chan map[string]any, 256)
	srv.tg = NewTelegram(fakeClearAPI(t, calls, fail...).URL, "TOKEN")
	now := time.Now().Unix()
	for i := 1; i <= n; i++ {
		if err := store.AddTgMessage(42, int64(i), now); err != nil {
			t.Fatal(err)
		}
	}
	return srv, store, calls
}

// drain collects the recorded calls of one method until none arrive for a bit.
func drain(t *testing.T, calls <-chan map[string]any, method string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for {
		select {
		case c := <-calls:
			if c["method"] == method {
				out = append(out, c)
			}
		case <-time.After(200 * time.Millisecond):
			return out
		}
	}
}

func ids(v any) string {
	var b strings.Builder
	for _, id := range v.([]any) {
		fmt.Fprintf(&b, "%d ", int(id.(float64)))
	}
	return strings.TrimSpace(b.String())
}

// TestClearCommand: /clear bulk-deletes every tracked id younger than 48h
// except the pinned dashboard, replies with silence plus the command
// message's deletion, and forgets the rows.
func TestClearCommand(t *testing.T) {
	srv, store, calls := newClearServer(t, 3)
	store.AddTgMessage(42, 100, time.Now().Unix()) // the pinned dashboard
	store.SetPin(42, 100, "clean")
	store.AddTgMessage(42, 101, time.Now().Unix())
	store.AddTgMessage(42, 0, time.Now().Add(-49*time.Hour).Unix()) // too old for telegram

	reply := srv.handleMessage(context.Background(), "/clear")
	if reply.Text != "" || !reply.DeleteInbound {
		t.Fatalf("/clear reply = %+v; want silence with the command message deleted", reply)
	}
	dels := drain(t, calls, "deleteMessages")
	if len(dels) != 1 || dels[0]["chat_id"] != float64(42) || ids(dels[0]["message_ids"]) != "1 2 3 101" {
		t.Fatalf("deleteMessages calls = %v; want one with ids 1 2 3 101", dels)
	}
	if got, _ := store.TgMessages(42); len(got) != 0 {
		t.Fatalf("tracked after clear = %v; want none", got)
	}
	if pins, _ := store.Pins(); len(pins) != 1 {
		t.Fatal("clear removed the pin row")
	}
}

// TestClearBatches: telegram takes 100 ids per deleteMessages call.
func TestClearBatches(t *testing.T) {
	srv, _, calls := newClearServer(t, 150)
	srv.handleMessage(context.Background(), "/clear")
	dels := drain(t, calls, "deleteMessages")
	if len(dels) != 2 || len(dels[0]["message_ids"].([]any)) != 100 || len(dels[1]["message_ids"].([]any)) != 50 {
		t.Fatalf("deleteMessages batches = %d; want 100 + 50", len(dels))
	}
}

// TestClearFallsBackPerMessage: a refused batch is retried id by id so one
// undeletable message doesn't keep the rest visible.
func TestClearFallsBackPerMessage(t *testing.T) {
	srv, store, calls := newClearServer(t, 3, "deleteMessages", "deleteMessage")
	srv.handleMessage(context.Background(), "/clear")
	dels := drain(t, calls, "deleteMessage")
	if len(dels) != 3 || dels[0]["message_id"] != float64(1) || dels[2]["message_id"] != float64(3) {
		t.Fatalf("deleteMessage fallbacks = %v; want ids 1..3", dels)
	}
	if got, _ := store.TgMessages(42); len(got) != 0 {
		t.Fatalf("tracked after failed clear = %v; want none — a message telegram refuses now never becomes deletable", got)
	}
}

// TestClearNothingTracked: no tracked ids means no telegram calls at all.
func TestClearNothingTracked(t *testing.T) {
	srv, _, calls := newClearServer(t, 0)
	srv.handleMessage(context.Background(), "/clear")
	if dels := drain(t, calls, "deleteMessages"); len(dels) != 0 {
		t.Fatalf("deleteMessages on nothing tracked = %v", dels)
	}
}

// TestClearOnSessionSwitch: /new, /agent and a /sessions resume tap clear
// the view too, before their own reply.
func TestClearOnSessionSwitch(t *testing.T) {
	srv, store, calls := newClearServer(t, 2)
	if r := srv.handleMessage(context.Background(), "/new"); r.Text != "" || !r.DeleteInbound {
		t.Fatalf("/new reply = %+v", r)
	}
	if dels := drain(t, calls, "deleteMessages"); len(dels) != 1 || ids(dels[0]["message_ids"]) != "1 2" {
		t.Fatalf("/new deleteMessages = %v; want ids 1 2", dels)
	}

	store.AddSession("a", false, "")
	store.SetSessionName("a", "trip planning")
	store.AddTgMessage(42, 3, time.Now().Unix())
	r := srv.gatedCallback(context.Background(), 42, "a")
	if !strings.Contains(r.Text, "resumed") || !strings.Contains(r.Text, "trip planning") {
		t.Fatalf("resume reply = %q", r.Text)
	}
	if dels := drain(t, calls, "deleteMessages"); len(dels) != 1 || ids(dels[0]["message_ids"]) != "3" {
		t.Fatalf("resume deleteMessages = %v; want id 3", dels)
	}
}

func TestClearOnAgentStart(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store.AddPairing("telegram", "42", "CODE")
	store.ApprovePairing("telegram", "CODE")
	calls := make(chan map[string]any, 32)
	srv.tg = NewTelegram(fakeClearAPI(t, calls).URL, "TOKEN")
	store.AddTgMessage(42, 9, time.Now().Unix())

	r := srv.handleMessage(context.Background(), "/agent")
	if !strings.Contains(r.Text, "agent session started") || !r.DeleteInbound {
		t.Fatalf("/agent reply = %+v; want the note with the command message deleted", r)
	}
	if dels := drain(t, calls, "deleteMessages"); len(dels) != 1 || ids(dels[0]["message_ids"]) != "9" {
		t.Fatalf("/agent deleteMessages = %v; want id 9", dels)
	}
}
