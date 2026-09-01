package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"app":"omni","version":"v1.2.3"}`)
	})
	mux.HandleFunc("GET /pairing/telegram", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"user_id":"99","code":"CODE1234","approved":false}]`)
	})
	mux.HandleFunc("POST /pairing/telegram/approve", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"user_id":"99","code":"CODE1234","approved":true}`)
	})
	mux.HandleFunc("POST /pairing/telegram/revoke", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("GET /channels", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"telegram","connected":true,"bot_username":"omni_bot"}]`)
	})
	mux.HandleFunc("GET /channels/telegram", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"telegram","connected":true,"bot_username":"omni_bot"}`)
	})
	mux.HandleFunc("POST /channels/telegram/connect", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"token_required"}`)
	})
	mux.HandleFunc("GET /llm", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"openai","connected":true,"source":"api_key"},{"name":"claude","connected":false},{"name":"gemini","connected":false}]`)
	})
	mux.HandleFunc("GET /llm/openai", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"name":"openai","connected":true,"source":"api_key"}`)
	})
	mux.HandleFunc("POST /llm/openai/connect", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"key_required"}`)
	})
	return httptest.NewServer(mux)
}

func TestClientStatus(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	st, err := c.Status()
	if err != nil || st.App != "omni" || st.Version != "v1.2.3" {
		t.Fatalf("Status = %v, %v", st, err)
	}
}

func TestClientPairings(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	ps, err := c.Pairings("telegram")
	if err != nil || len(ps) != 1 || ps[0].UserID != "99" || ps[0].Code != "CODE1234" || ps[0].Approved {
		t.Fatalf("Pairings = %v, %v", ps, err)
	}

	p, err := c.ApprovePairing("telegram", "CODE1234")
	if err != nil || p.UserID != "99" || !p.Approved {
		t.Fatalf("ApprovePairing = %v, %v", p, err)
	}

	if err := c.RevokePairing("telegram", "99"); err != nil {
		t.Fatalf("RevokePairing = %v", err)
	}
}

func TestClientChannels(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	chs, err := c.Channels()
	if err != nil || len(chs) != 1 || chs[0].Name != "telegram" || !chs[0].Connected {
		t.Fatalf("Channels = %v, %v", chs, err)
	}

	ch, err := c.Channel("telegram")
	if err != nil || ch.BotUsername != "omni_bot" {
		t.Fatalf("Channel = %v, %v", ch, err)
	}
}

func TestClientConnectTokenRequired(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	_, err := c.Connect("telegram", "")
	if !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("Connect error = %v, want ErrTokenRequired", err)
	}
}

func TestClientLLMs(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	ls, err := c.LLMs()
	if err != nil || len(ls) != 3 || ls[0].Name != "openai" || !ls[0].Connected || ls[0].Source != "api_key" {
		t.Fatalf("LLMs = %v, %v", ls, err)
	}

	l, err := c.LLM("openai")
	if err != nil || l.Source != "api_key" {
		t.Fatalf("LLM = %v, %v", l, err)
	}
}

func TestClientConnectLLMKeyRequired(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	_, err := c.ConnectLLM("openai", "")
	if !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("ConnectLLM error = %v, want ErrKeyRequired", err)
	}
}

func TestClientUnknownChannel(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	if _, err := c.Channel("smoke-signals"); err == nil {
		t.Fatal("Channel(unknown) = nil error, want error")
	}
}
