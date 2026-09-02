package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *Store) {
	t.Helper()
	// isolate token sources
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// fake telegram: token GOOD works, anything else is unauthorized
	mux := http.NewServeMux()
	mux.HandleFunc("/botGOOD/getMe", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"username":"omni_test_bot"}}`)
	})
	mux.HandleFunc("/botGOOD/getUpdates", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	})
	mux.HandleFunc("/botGOOD/setMyCommands", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"description":"Unauthorized"}`)
	})
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)

	store, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := NewServer(store, fake.URL)
	t.Cleanup(srv.Close)
	return srv, store
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any, []map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	raw := w.Body.Bytes()
	var obj map[string]any
	var list []map[string]any
	if len(raw) > 0 && raw[0] == '[' {
		json.Unmarshal(raw, &list)
	} else {
		json.Unmarshal(raw, &obj)
	}
	return w.Code, obj, list
}

func TestStatusEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)
	code, obj, _ := doJSON(t, srv.Handler(), "GET", "/status", "")
	if code != 200 || obj["app"] != "omni" || obj["version"] != version {
		t.Fatalf("GET /status = %d, %v; want 200 with app and version", code, obj)
	}
}

func TestChannelsListDisconnected(t *testing.T) {
	srv, _ := newTestServer(t)
	code, _, list := doJSON(t, srv.Handler(), "GET", "/channels", "")
	if code != 200 || len(list) != 1 {
		t.Fatalf("GET /channels = %d, %v; want 200 with 1 channel", code, list)
	}
	if list[0]["name"] != "telegram" || list[0]["connected"] != false {
		t.Fatalf("channel = %v, want telegram disconnected", list[0])
	}
}

func TestChannelDetail(t *testing.T) {
	srv, _ := newTestServer(t)
	code, obj, _ := doJSON(t, srv.Handler(), "GET", "/channels/telegram", "")
	if code != 200 || obj["name"] != "telegram" || obj["connected"] != false {
		t.Fatalf("GET /channels/telegram = %d, %v", code, obj)
	}
}

func TestConnectWithoutToken(t *testing.T) {
	srv, _ := newTestServer(t)
	code, obj, _ := doJSON(t, srv.Handler(), "POST", "/channels/telegram/connect", "{}")
	if code != 400 || obj["error"] != "token_required" {
		t.Fatalf("connect without token = %d, %v; want 400 token_required", code, obj)
	}
}

func TestConnectBadToken(t *testing.T) {
	srv, _ := newTestServer(t)
	code, obj, _ := doJSON(t, srv.Handler(), "POST", "/channels/telegram/connect", `{"token":"BAD"}`)
	if code != 401 {
		t.Fatalf("connect with bad token = %d, %v; want 401", code, obj)
	}
}

func TestConnectHappyPath(t *testing.T) {
	srv, store := newTestServer(t)
	h := srv.Handler()

	code, obj, _ := doJSON(t, h, "POST", "/channels/telegram/connect", `{"token":"GOOD"}`)
	if code != 200 || obj["connected"] != true || obj["bot_username"] != "omni_test_bot" {
		t.Fatalf("connect = %d, %v; want 200 connected with bot_username", code, obj)
	}

	// status now reflects the live connection
	_, obj, _ = doJSON(t, h, "GET", "/channels/telegram", "")
	if obj["connected"] != true || obj["bot_username"] != "omni_test_bot" {
		t.Fatalf("detail after connect = %v", obj)
	}

	// intent persisted for resume-on-restart
	if ok, _ := store.Connected("telegram"); !ok {
		t.Fatal("store not marked connected")
	}
}
