package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// shim writes an executable into the newUpdateTestServer PATH dir (derived
// from its systemctl log path) so ops actions exec fakes, not real tools.
func shim(t *testing.T, sysLog, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(filepath.Dir(sysLog), name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestOpsMenu(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	r := srv.handleMessage(context.Background(), "/ops")
	if !strings.Contains(r.Text, "🛠 omni") {
		t.Fatalf("menu = %+v", r)
	}
	var datas []string
	for _, row := range r.Keyboard {
		for _, b := range row {
			datas = append(datas, b.CallbackData)
		}
	}
	want := []string{"ops:status", "ops:doctor", "ops:logs", "ops:disk", "ops:restart", "ops:update", "ops:terminal"}
	if !slices.Equal(datas, want) {
		t.Fatalf("callbacks = %v", datas)
	}
}

func TestOpsStatus(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	r := srv.gatedCallback(context.Background(), 42, "ops:status")
	if !strings.Contains(r.Text, version) || !strings.Contains(r.Text, "▶") {
		t.Fatalf("status = %q", r.Text)
	}
}

func TestOpsLogsRedactsToken(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)
	shim(t, sysLog, "journalctl", "#!/bin/sh\necho 'post http://api/bot123456:AAAA-secret/send failed'\necho 'line two'\n")
	r := srv.gatedCallback(context.Background(), 42, "ops:logs")
	if !strings.Contains(r.Text, "bot***") || strings.Contains(r.Text, "AAAA-secret") {
		t.Fatalf("token not redacted: %q", r.Text)
	}
	if !strings.Contains(r.Text, "line two") {
		t.Fatalf("log lines missing: %q", r.Text)
	}
}

func TestOpsDisk(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	os.MkdirAll(dataDir(), 0o700)
	r := srv.gatedCallback(context.Background(), 42, "ops:disk")
	if !strings.Contains(r.Text, "free") {
		t.Fatalf("disk = %q", r.Text)
	}
}

func TestOpsRestartConfirm(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)

	r := srv.gatedCallback(context.Background(), 42, "ops:restart")
	if len(r.Keyboard) != 1 || r.Keyboard[0][0].CallbackData != "ops:restart!" || r.Keyboard[0][1].CallbackData != "ops:cancel" {
		t.Fatalf("confirm = %+v", r)
	}
	if r := srv.gatedCallback(context.Background(), 42, "ops:cancel"); !r.StripKeyboard || !strings.Contains(r.Text, "cancelled") {
		t.Fatalf("cancel = %+v", r)
	}

	shim(t, sysLog, "systemd-run", "#!/bin/sh\necho \"$@\" >> "+sysLog+"\n")
	r = srv.gatedCallback(context.Background(), 42, "ops:restart!")
	if !r.StripKeyboard || !strings.Contains(r.Text, "restarting") {
		t.Fatalf("restart = %+v", r)
	}
	logData, _ := os.ReadFile(sysLog)
	if !strings.Contains(string(logData), "--on-active=2s") || !strings.Contains(string(logData), "restart "+app+"-server.service") {
		t.Fatalf("systemd-run log = %q", logData)
	}
}

func TestOpsUpdateKicksGuardian(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)
	os.MkdirAll(dataDir(), 0o700)
	os.WriteFile(filepath.Join(dataDir(), "updates.stamp"), nil, 0o600)
	os.WriteFile(filepath.Join(dataDir(), "update.ignore"), []byte("v9.9.9"), 0o600)

	r := srv.gatedCallback(context.Background(), 42, "ops:update")
	if !strings.Contains(r.Text, "🔎") {
		t.Fatalf("update = %q", r.Text)
	}
	for _, f := range []string{"updates.stamp", "update.ignore"} {
		if _, err := os.Stat(filepath.Join(dataDir(), f)); err == nil {
			t.Fatalf("%s survived; a manual check must un-throttle and un-ignore", f)
		}
	}
	logData, _ := os.ReadFile(sysLog)
	if want := "start --no-block " + app + "-guardian.service"; !strings.Contains(string(logData), want) {
		t.Fatalf("systemctl log = %q; want %q", logData, want)
	}
}

func TestOpsTerminalEntry(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	if r := srv.gatedCallback(context.Background(), 42, "ops:terminal"); !strings.Contains(r.Text, "sudo password") {
		t.Fatalf("entry = %q", r.Text)
	}
	// second tap while awaiting the password must not double-arm
	if r := srv.gatedCallback(context.Background(), 42, "ops:terminal"); !strings.Contains(r.Text, "already") {
		t.Fatalf("re-entry = %q", r.Text)
	}
}

func TestOpsUnknownAction(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	// must not fall through to resumeSession's "session not found"
	if r := srv.gatedCallback(context.Background(), 42, "ops:bogus"); !strings.Contains(r.Text, "⚠") {
		t.Fatalf("unknown = %q", r.Text)
	}
}

func TestOpsNeedsApproval(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	if r := srv.gatedCallback(context.Background(), 99, "ops:restart!"); r.Text != "" {
		t.Fatalf("unapproved sender must get silence, got %q", r.Text)
	}
}
