package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testAPIToken = "test-api-token"

func authReq(method, path, token, origin string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(`{"code":"X"}`))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestAPIRequiresBearer(t *testing.T) {
	srv, store := newTestServer(t)
	if err := store.AddPairing("telegram", "424242", "AUDITCODE"); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	cases := []struct {
		name         string
		req          *http.Request
		want         int
		bodyMustMiss string
	}{
		{"status open", authReq("GET", "/status", "", ""), 200, ""},
		{"list without token", authReq("GET", "/pairing/telegram", "", ""), 401, "AUDITCODE"},
		{"list wrong token", authReq("GET", "/pairing/telegram", "nope", ""), 401, "AUDITCODE"},
		{"approve without token", authReq("POST", "/pairing/telegram/approve", "", ""), 401, ""},
		{"approve cross-origin", authReq("POST", "/pairing/telegram/approve", testAPIToken, "https://evil.invalid"), 403, ""},
		{"list with token", authReq("GET", "/pairing/telegram", testAPIToken, ""), 200, ""},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, c.req)
		if w.Code != c.want {
			t.Errorf("%s: code %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
		if c.bodyMustMiss != "" && strings.Contains(w.Body.String(), c.bodyMustMiss) {
			t.Errorf("%s: body leaks %q", c.name, c.bodyMustMiss)
		}
	}
	if p, _, _ := store.Pairing("telegram", "424242"); p.Approved {
		t.Fatal("pairing approved by an unauthenticated request")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authReq("POST", "/pairing/telegram/approve", testAPIToken, ""))
	if w.Code != 404 { // valid auth, unknown code: reaches the handler
		t.Fatalf("authorised approve = %d, want 404 unknown_code", w.Code)
	}
}

func TestAPIPostNeedsJSON(t *testing.T) {
	srv, _ := newTestServer(t)
	req := authReq("POST", "/pairing/telegram/approve", testAPIToken, "")
	req.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain POST = %d, want 415", w.Code)
	}
}

func TestAPITokenFileCreated(t *testing.T) {
	t.Setenv("OMNI_API_TOKEN", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	tok, err := loadOrCreateAPIToken()
	if err != nil || len(tok) != 64 {
		t.Fatalf("token %q, err %v", tok, err)
	}
	path := filepath.Join(os.Getenv("XDG_DATA_HOME"), app, "api-token")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v, mode %v", err, st.Mode())
	}
	again, _ := loadOrCreateAPIToken()
	if again != tok {
		t.Fatal("second load generated a new token")
	}
}

func TestListenAddrLoopbackByDefault(t *testing.T) {
	t.Setenv("OMNI_ADDR", "")
	t.Setenv("OMNI_CONTAINER", "")
	if got := listenAddr(); got != "127.0.0.1"+defaultAddr {
		t.Fatalf("native default = %q", got)
	}
	t.Setenv("OMNI_CONTAINER", "1")
	if got := listenAddr(); got != defaultAddr {
		t.Fatalf("container default = %q, want wildcard %q", got, defaultAddr)
	}
	t.Setenv("OMNI_ADDR", "0.0.0.0:9000")
	t.Setenv("OMNI_CONTAINER", "")
	if got := listenAddr(); got != "0.0.0.0:9000" {
		t.Fatalf("explicit addr rewritten: %q", got)
	}
}
