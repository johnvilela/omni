package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newUpdateTestServer: minimal server with one approved pairing (42), an
// isolated data dir and a systemctl PATH shim logging its args.
func newUpdateTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, store := newTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := store.AddPairing("telegram", "42", "CODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePairing("telegram", "CODE"); err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	sysLog := filepath.Join(shim, "log")
	if err := os.WriteFile(filepath.Join(shim, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> "+sysLog+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim)
	return srv, sysLog
}

func TestStartUpdateCallback(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)
	r := srv.gatedCallback(context.Background(), 42, "upd:v9.9.9")
	if !strings.Contains(r.Text, "⏳") || !r.StripKeyboard {
		t.Fatalf("reply = %+v; want ⏳ + StripKeyboard", r)
	}
	data, err := os.ReadFile(filepath.Join(dataDir(), "update.request"))
	if err != nil || string(data) != "v9.9.9" {
		t.Fatalf("update.request = %q, %v; want the tapped tag", data, err)
	}
	logData, _ := os.ReadFile(sysLog)
	if want := "--user start --no-block " + app + "-guardian.service"; !strings.Contains(string(logData), want) {
		t.Fatalf("systemctl log = %q; want %q", logData, want)
	}
}

func TestStartUpdateBadTag(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	r := srv.gatedCallback(context.Background(), 42, "upd:../evil")
	if !strings.Contains(r.Text, "⚠") {
		t.Fatalf("reply = %+v; want a rejection", r)
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "update.request")); err == nil {
		t.Fatal("update.request written for a bad tag")
	}
}

func TestUpdateChangelog(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/omni/releases/tags/v9.9.9" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"body":"- fixed the flux capacitor"}`)
	}))
	defer fake.Close()
	t.Setenv("OMNI_GITHUB_API", fake.URL)

	// no update_repos configured: no repo to ask
	if r := srv.gatedCallback(context.Background(), 42, "updlog:v9.9.9"); !strings.Contains(r.Text, "⚠") {
		t.Fatalf("reply = %+v; want ⚠ without update_repos", r)
	}

	dir, _ := os.UserConfigDir()
	os.MkdirAll(filepath.Join(dir, app), 0o700)
	os.WriteFile(filepath.Join(dir, app, "config.yaml"), []byte("update_repos: [o/omni]\n"), 0o600)
	r := srv.gatedCallback(context.Background(), 42, "updlog:v9.9.9")
	if !strings.Contains(r.Text, "fixed the flux capacitor") {
		t.Fatalf("reply = %+v; want the release notes", r)
	}
	if r.StripKeyboard {
		t.Fatal("changelog stripped the keyboard; Update/Ignore must stay live")
	}
}

func TestIgnoreUpdate(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	os.MkdirAll(dataDir(), 0o700)
	os.WriteFile(filepath.Join(dataDir(), "updates.stamp"), nil, 0o600)

	r := srv.gatedCallback(context.Background(), 42, "updign:v9.9.9")
	if !strings.Contains(r.Text, "🙈") || !r.StripKeyboard {
		t.Fatalf("reply = %+v; want 🙈 + StripKeyboard", r)
	}
	data, err := os.ReadFile(filepath.Join(dataDir(), "update.ignore"))
	if err != nil || string(data) != "v9.9.9" {
		t.Fatalf("update.ignore = %q, %v; want the ignored tag", data, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "updates.stamp")); err == nil {
		t.Fatal("updates.stamp survived; ignore must un-throttle the guardian")
	}
}

func TestUpdateCallbacksNeedApproval(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	if r := srv.gatedCallback(context.Background(), 99, "upd:v9.9.9"); r.Text != "" {
		t.Fatalf("reply = %+v; unapproved sender must get silence", r)
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "update.request")); err == nil {
		t.Fatal("update.request written for an unapproved sender")
	}
}
