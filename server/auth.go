package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The localhost API is the administration surface: it approves pairings and
// connects bots and providers. Every route except GET /status requires a
// bearer token the server generates on first start. The cli, the guardian
// bootstrap and the docker entrypoint read the same file; OMNI_API_TOKEN
// overrides it (tests, containers).

// apiTokenPath is <data dir>/api-token, next to omni.db.
func apiTokenPath() string {
	return filepath.Join(filepath.Dir(dbPath()), "api-token")
}

// loadOrCreateAPIToken returns the token from OMNI_API_TOKEN or the token
// file, generating and persisting (0600) a fresh one when neither exists.
func loadOrCreateAPIToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("OMNI_API_TOKEN")); t != "" {
		registerSecret(t)
		return t, nil
	}
	path := apiTokenPath()
	if raw, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(raw)); t != "" {
			registerSecret(t)
			return t, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	t := hex.EncodeToString(buf)
	registerSecret(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// requireAuth wraps the API mux. GET /status stays open so health probes
// (guardian, docker HEALTHCHECK) need no secret. Everything else needs
// `Authorization: Bearer <token>`; requests carrying an Origin header are
// browser cross-origin attempts and are refused regardless of token.
func requireAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/status" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Origin") != "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross_origin"})
			return
		}
		if token == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no_api_token"})
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if r.Method == http.MethodPost {
			if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "json_required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// listenAddr resolves OMNI_ADDR / defaultAddr. A bare ":port" means loopback
// on a native install; inside the container it stays wildcard because compose
// publishes the port (on 127.0.0.1 of the host) through the bridge.
func listenAddr() string {
	addr := os.Getenv("OMNI_ADDR")
	if addr == "" {
		addr = defaultAddr
	}
	if strings.HasPrefix(addr, ":") && os.Getenv("OMNI_CONTAINER") != "1" {
		addr = "127.0.0.1" + addr
	}
	return addr
}

func apiTokenHint() string {
	return fmt.Sprintf("api token: %s (OMNI_API_TOKEN overrides)", apiTokenPath())
}
