package main

// Container mode (OMNI_CONTAINER=1): doctor and the guardian subcommands have
// no systemd to ask, so they check processes and point at .env instead.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallScriptInContainer(t *testing.T) {
	t.Setenv("OMNI_CONTAINER", "1")
	if got := installScript(); got != "docker compose pull && docker compose up -d" {
		t.Fatalf("installScript() in docker = %q", got)
	}
	if got := serverRestartFix(); got != "docker compose restart omni" {
		t.Fatalf("serverRestartFix() in docker = %q", got)
	}
	t.Setenv("OMNI_CONTAINER", "")
	if got := installScript(); got != installOneLiner {
		t.Fatalf("installScript() on host = %q", got)
	}
}

// pgrepShim makes pgrep answer $PGREP_EXIT (default 0 = running) and is the
// only thing on PATH.
func pgrepShim(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pgrep"), []byte("#!/bin/sh\nexit ${PGREP_EXIT:-0}\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestServiceChecksInContainer(t *testing.T) {
	t.Setenv("OMNI_CONTAINER", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	pgrepShim(t)

	cs := serviceChecks()
	if len(cs) != 4 || failCount(cs) != 0 {
		t.Fatalf("all running = %+v; want 3 process checks + no alerts, all green", cs)
	}
	for _, c := range cs {
		if strings.Contains(c.name, "systemctl") || strings.Contains(c.fix, "systemctl") {
			t.Fatalf("check %+v mentions systemctl in docker", c)
		}
	}

	t.Setenv("PGREP_EXIT", "1")
	cs = serviceChecks()
	if failCount(cs) != 3 {
		t.Fatalf("nothing running = %+v; want 3 failures", cs)
	}
	if cs[0].fix != "docker compose logs --tail 50 omni" {
		t.Fatalf("fix = %q; want the compose logs hint", cs[0].fix)
	}

	t.Setenv("PGREP_EXIT", "0")
	os.MkdirAll(dataDir(), 0o700)
	os.WriteFile(filepath.Join(dataDir(), "guardian.json"), []byte(`{"disk":"2026-09-02T12:00:00Z"}`), 0o600)
	cs = serviceChecks()
	last := cs[len(cs)-1]
	if !strings.Contains(last.name, "guardian alert: disk") || last.fix != "docker compose logs --tail 50 omni" {
		t.Fatalf("alert check = %+v; want the disk alert with the compose logs fix", last)
	}
}

func TestGuardianCommandsInContainer(t *testing.T) {
	t.Setenv("OMNI_CONTAINER", "1")
	if code := runGuardianInterval("5m"); code != 2 {
		t.Fatalf("set-interval in docker = %d; want 2 with the .env hint", code)
	}
	if code := runGuardianEnable(false); code != 2 {
		t.Fatalf("--enabled=false in docker = %d; want 2 with the .env hint", code)
	}
}

func TestContainerGuardianLine(t *testing.T) {
	for _, tc := range []struct {
		every   string
		running bool
		want    string
	}{
		{"2m", false, "loop not running"},
		{"off", true, "disabled"},
		{"0", true, "disabled"},
		{"2m", true, "every 2m"},
	} {
		if got := containerGuardianLine(tc.every, tc.running); !strings.Contains(got, tc.want) {
			t.Errorf("containerGuardianLine(%q, %v) = %q; want %q", tc.every, tc.running, got, tc.want)
		}
	}
}
