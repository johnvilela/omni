package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTelegram serves getMe, firstUpdates on the first getUpdates, then
// empties. sendChatAction bodies go to actions, answerCallbackQuery bodies to
// answered, when non-nil.
func fakeTelegram(t *testing.T, sent chan<- map[string]any, actions, answered chan<- map[string]any, firstUpdates string) *httptest.Server {
	var calls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/getMe", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"username":"omni_test_bot"}}`)
	})
	mux.HandleFunc("/botTOKEN/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true,"result":[`+firstUpdates+`]}`)
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
	mux.HandleFunc("/botTOKEN/answerCallbackQuery", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if answered != nil {
			answered <- body
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

const helloUpdate = `{"update_id":7,"message":{"chat":{"id":42},"from":{"id":99},"text":"hello"}}`

func TestTelegramPollSkipsEmptyReply(t *testing.T) {
	sent := make(chan map[string]any, 1)
	srv := fakeTelegram(t, sent, nil, nil, helloUpdate)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg := NewTelegram(srv.URL, "TOKEN")
	answered := make(chan struct{})
	tg.answer = func(context.Context, int64, string) tgReply {
		close(answered)
		return tgReply{} // rate-limited senders get silence, not an empty sendMessage
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
	srv := fakeTelegram(t, nil, nil, nil, helloUpdate)
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
	srv := fakeTelegram(t, sent, actions, nil, helloUpdate)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	tg := NewTelegram(srv.URL, "TOKEN")
	// blocking on the typing action proves the indicator fires while answering
	tg.answer = func(_ context.Context, from int64, text string) tgReply {
		if from != 99 {
			t.Errorf("answer from = %d, want 99 (the sender id, not the chat)", from)
		}
		a := <-actions
		if a["chat_id"] != float64(42) || a["action"] != "typing" {
			t.Errorf("sendChatAction body = %v, want chat_id 42 typing", a)
		}
		return tgReply{Text: "echo:" + text}
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

// TestTelegramRegisterCommands locks the "/" autocomplete menu payload.
func TestTelegramRegisterCommands(t *testing.T) {
	got := make(chan map[string]any, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/setMyCommands", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got <- body
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tg := NewTelegram(srv.URL, "TOKEN")
	if err := tg.registerCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	cmds, _ := (<-got)["commands"].([]any)
	want := []string{"new", "agent", "sessions"}
	if len(cmds) != len(want) {
		t.Fatalf("registered %d commands; want %d", len(cmds), len(want))
	}
	for i, c := range cmds {
		m := c.(map[string]any)
		if m["command"] != want[i] || m["description"] == "" {
			t.Fatalf("command %d = %v; want /%s with a description", i, m, want[i])
		}
	}
}

// TestTelegramCallbackQuery: a button tap reaches the callback hook and its
// reply lands in the tapped message's chat, after the spinner is answered.
func TestTelegramCallbackQuery(t *testing.T) {
	sent := make(chan map[string]any, 1)
	answered := make(chan map[string]any, 1)
	srv := fakeTelegram(t, sent, nil, answered,
		`{"update_id":7,"callback_query":{"id":"cb1","from":{"id":99},"message":{"chat":{"id":42}},"data":"sess-1"}}`)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg := NewTelegram(srv.URL, "TOKEN")
	tg.answer = func(context.Context, int64, string) tgReply { return tgReply{} }
	tg.callback = func(_ context.Context, from int64, data string) tgReply {
		if from != 99 || data != "sess-1" {
			t.Errorf("callback from=%d data=%q, want 99 sess-1", from, data)
		}
		return tgReply{Text: "resumed"}
	}
	go tg.Poll(ctx)

	select {
	case body := <-answered:
		if body["callback_query_id"] != "cb1" {
			t.Fatalf("answerCallbackQuery body = %v, want cb1", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answerCallbackQuery within 3s")
	}
	select {
	case body := <-sent:
		if body["chat_id"] != float64(42) || body["text"] != "resumed" {
			t.Fatalf("sendMessage body = %v, want chat_id 42 resumed", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sendMessage within 3s")
	}
}

// TestTelegramKeyboardMarkup: a reply carrying buttons sends reply_markup.
func TestTelegramKeyboardMarkup(t *testing.T) {
	sent := make(chan map[string]any, 1)
	srv := fakeTelegram(t, sent, nil, nil, helloUpdate)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg := NewTelegram(srv.URL, "TOKEN")
	tg.answer = func(context.Context, int64, string) tgReply {
		return tgReply{Text: "sessions:", Keyboard: [][]button{{{Text: "🤖 fix bug", CallbackData: "sess-1"}}}}
	}
	go tg.Poll(ctx)

	select {
	case body := <-sent:
		raw, _ := json.Marshal(body["reply_markup"])
		if string(raw) != `{"inline_keyboard":[[{"callback_data":"sess-1","text":"🤖 fix bug"}]]}` {
			t.Fatalf("reply_markup = %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sendMessage within 3s")
	}
}

// TestTelegramSendChunks: replies over telegram's 4096-char cap arrive as
// multiple messages, in order, keyboard on the last only.
func TestTelegramSendChunks(t *testing.T) {
	sent := make(chan map[string]any, 2)
	srv := fakeTelegram(t, sent, nil, nil, helloUpdate)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg := NewTelegram(srv.URL, "TOKEN")
	long := strings.Repeat("a", 4000) + strings.Repeat("é", 100) // é: 2 bytes, forces a rune-boundary cut
	tg.answer = func(context.Context, int64, string) tgReply {
		return tgReply{Text: long, Keyboard: [][]button{{{Text: "b", CallbackData: "d"}}}}
	}
	go tg.Poll(ctx)

	var got string
	first := <-sent
	if first["reply_markup"] != nil {
		t.Fatalf("first chunk carries the keyboard; want it on the last only")
	}
	got += first["text"].(string)
	select {
	case second := <-sent:
		got += second["text"].(string)
		if second["reply_markup"] == nil {
			t.Fatal("last chunk missing the keyboard")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no second chunk within 3s")
	}
	if got != long {
		t.Fatalf("reassembled chunks != original (len %d vs %d)", len(got), len(long))
	}
}
