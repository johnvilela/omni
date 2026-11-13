package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClientSendsBearerFromFile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("OMNI_API_TOKEN", "")
	os.MkdirAll(filepath.Join(data, app), 0o700)
	os.WriteFile(filepath.Join(data, app, "api-token"), []byte("filetoken\n"), 0o600)
	var got string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Write([]byte(`[]`))
	}))
	defer fake.Close()
	if _, err := NewClient(fake.URL).Channels(); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer filetoken" {
		t.Fatalf("Authorization = %q", got)
	}
	t.Setenv("OMNI_API_TOKEN", "envtoken")
	NewClient(fake.URL).Channels()
	if got != "Bearer envtoken" {
		t.Fatalf("env override: Authorization = %q", got)
	}
}
