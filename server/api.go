package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
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

	pairMu   sync.Mutex
	pairHits map[int64]*pairHit

	digesting atomic.Bool // one long-term memory digest in flight at a time
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
	tg.answer = s.gatedAnswer
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
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"app": app, "version": version})
	})
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
	mux.HandleFunc("GET /pairing/telegram", func(w http.ResponseWriter, r *http.Request) {
		ps, err := s.store.Pairings("telegram")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if ps == nil {
			ps = []Pairing{}
		}
		writeJSON(w, http.StatusOK, ps)
	})
	mux.HandleFunc("POST /pairing/telegram/approve", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		id, err := s.store.ApprovePairing("telegram", body.Code)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if id == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_code"})
			return
		}
		writeJSON(w, http.StatusOK, Pairing{UserID: id, Code: body.Code, Approved: true})
	})
	mux.HandleFunc("POST /pairing/telegram/revoke", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UserID string `json:"user_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		ok, err := s.store.RevokePairing("telegram", body.UserID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_user"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
	})
	mux.HandleFunc("GET /llm", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.llmStatuses())
	})
	mux.HandleFunc("GET /llm/{provider}", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("provider")
		if !slices.Contains(llmProviders, p) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_provider"})
			return
		}
		for _, ls := range s.llmStatuses() {
			if ls.Name == p {
				writeJSON(w, http.StatusOK, ls)
				return
			}
		}
	})
	mux.HandleFunc("POST /llm/{provider}/connect", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("provider")
		if !slices.Contains(llmProviders, p) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_provider"})
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		json.NewDecoder(r.Body).Decode(&body) // empty body is fine
		status, code, err := s.ConnectLLM(r.Context(), p, body.Key)
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
