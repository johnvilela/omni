package main

// Container mode (OMNI_CONTAINER=1): /ops and the update taps cannot reach
// systemd, so they exit, hint or run the guardian binary directly.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpsRestartInContainer(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)
	t.Setenv("OMNI_CONTAINER", "1")
	got := make(chan int, 1)
	oldExit, oldDelay := exitFn, restartDelay
	exitFn, restartDelay = func(code int) { got <- code }, time.Millisecond
	t.Cleanup(func() { exitFn, restartDelay = oldExit, oldDelay })

	r := srv.gatedCallback(context.Background(), 42, "ops:restart!")
	if !strings.Contains(r.Text, "restarting") || !r.StripKeyboard {
		t.Fatalf("restart = %+v; want a restarting notice with the keyboard stripped", r)
	}
	select {
	case code := <-got:
		if code != 0 {
			t.Fatalf("exit code %d; want 0 (the entrypoint respawns on any exit)", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never exited")
	}
	if logData, _ := os.ReadFile(sysLog); strings.Contains(string(logData), "systemd-run") {
		t.Fatalf("shim log %q; docker must not call systemd-run", logData)
	}
}

func TestOpsLogsInContainer(t *testing.T) {
	srv, _ := newUpdateTestServer(t)
	t.Setenv("OMNI_CONTAINER", "1")
	r := srv.gatedCallback(context.Background(), 42, "ops:logs")
	if !strings.Contains(r.Text, "docker compose logs") {
		t.Fatalf("logs = %q; want the docker hint", r.Text)
	}
}

// TestOpsUpdateInContainer: the guardian binary runs as a oneshot child —
// without OMNI_GUARDIAN_INTERVAL, or it would start a second loop.
func TestOpsUpdateInContainer(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)
	t.Setenv("OMNI_CONTAINER", "1")
	t.Setenv("OMNI_GUARDIAN_INTERVAL", "2m")
	shim(t, sysLog, app+"-guardian", "#!/bin/sh\necho \"guardian interval=${OMNI_GUARDIAN_INTERVAL-unset}\" >> "+sysLog+"\n")
	os.MkdirAll(dataDir(), 0o700)
	os.WriteFile(filepath.Join(dataDir(), "updates.stamp"), nil, 0o600)

	r := srv.gatedCallback(context.Background(), 42, "ops:update")
	if !strings.Contains(r.Text, "🔎") || !strings.Contains(r.Text, "docker compose pull") {
		t.Fatalf("update = %q", r.Text)
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "updates.stamp")); err == nil {
		t.Fatal("updates.stamp survived; a manual check must un-throttle")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		logData, _ := os.ReadFile(sysLog)
		if strings.Contains(string(logData), "interval=unset") {
			if strings.Contains(string(logData), "start --no-block") {
				t.Fatalf("shim log %q; docker must not call systemctl", logData)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("guardian oneshot never ran (log %q)", logData)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartUpdateInContainer(t *testing.T) {
	srv, sysLog := newUpdateTestServer(t)
	t.Setenv("OMNI_CONTAINER", "1")
	r := srv.gatedCallback(context.Background(), 42, "upd:v9.9.9")
	if !strings.Contains(r.Text, "docker compose pull") || !r.StripKeyboard {
		t.Fatalf("reply = %+v; want the docker hint with the keyboard stripped", r)
	}
	if _, err := os.Stat(filepath.Join(dataDir(), "update.request")); err == nil {
		t.Fatal("update.request written in docker; nothing would ever claim it")
	}
	if logData, _ := os.ReadFile(sysLog); strings.Contains(string(logData), "start") {
		t.Fatalf("shim log %q; docker must not start the guardian unit", logData)
	}
}

func TestRestartAIMemoryInContainer(t *testing.T) {
	t.Setenv("OMNI_CONTAINER", "1")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit ${PKILL_EXIT:-0}\n"
	if err := os.WriteFile(filepath.Join(dir, "pkill"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if err := restartAIMemory(); err != nil {
		t.Fatalf("restartAIMemory = %v; want nil when pkill hits", err)
	}
	t.Setenv("PKILL_EXIT", "1")
	if err := restartAIMemory(); err != nil {
		t.Fatalf("restartAIMemory = %v; nothing running is not an error", err)
	}
	t.Setenv("PKILL_EXIT", "2")
	if err := restartAIMemory(); err == nil {
		t.Fatal("restartAIMemory = nil on a pkill failure; want an error")
	}
	if logData, _ := os.ReadFile(logPath); !strings.Contains(string(logData), "-x ai-memory") {
		t.Fatalf("shim log %q; want pkill -x ai-memory", logData)
	}
}
