package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
)

type channelStatus struct {
	Name        string `json:"name"`
	Connected   bool   `json:"connected"`
	BotUsername string `json:"bot_username,omitempty"`
}

// Server owns the channel state and the localhost HTTP API.
type Server struct {
	store   *Store
	apiBase string

	mu      sync.Mutex
	cancel  context.CancelFunc
	botUser string
}

func NewServer(store *Store, telegramAPIBase string) *Server {
	return &Server{store: store, apiBase: telegramAPIBase}
}

func (s *Server) telegramStatus() channelStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return channelStatus{Name: "telegram", Connected: s.cancel != nil, BotUsername: s.botUser}
}

// ConnectTelegram validates the token (request > env > config), starts the
// poller and persists the connected state. Empty token argument means
// "resolve from env/config".
func (s *Server) ConnectTelegram(ctx context.Context, reqToken string) (channelStatus, int, error) {
	token := ResolveToken(reqToken)
	if token == "" {
		return channelStatus{}, http.StatusBadRequest, errTokenRequired
	}
	tg := NewTelegram(s.apiBase, token)
	username, err := tg.GetMe(ctx)
	if err != nil {
		return channelStatus{}, http.StatusUnauthorized, err
	}

	s.mu.Lock()
	if s.cancel != nil {
		s.cancel() // reconnect replaces the old poller
	}
	pollCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.botUser = username
	s.mu.Unlock()
	go tg.Poll(pollCtx)

	if err := s.store.SetConnected("telegram", true); err != nil {
		return channelStatus{}, http.StatusInternalServerError, err
	}
	return s.telegramStatus(), http.StatusOK, nil
}

type apiError struct{ msg string }

func (e apiError) Error() string { return e.msg }

var errTokenRequired = apiError{"token_required"}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /channels", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, []channelStatus{s.telegramStatus()})
	})
	mux.HandleFunc("GET /channels/telegram", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.telegramStatus())
	})
	mux.HandleFunc("POST /channels/telegram/connect", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&body) // empty body is fine
		status, code, err := s.ConnectTelegram(r.Context(), body.Token)
		if err != nil {
			writeJSON(w, code, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, code, status)
	})
	return mux
}

// Close stops any running poller.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}
