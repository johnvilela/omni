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
	return httptest.NewServer(mux)
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

func TestClientUnknownChannel(t *testing.T) {
	srv := fakeServer()
	defer srv.Close()
	c := NewClient(srv.URL)

	if _, err := c.Channel("smoke-signals"); err == nil {
		t.Fatal("Channel(unknown) = nil error, want error")
	}
}
