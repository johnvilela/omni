package main

// Container mode (OMNI_CONTAINER=1): no systemd, so the guardian loops on
// OMNI_GUARDIAN_INTERVAL, heals with pkill and announces omni updates as text.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"omni/version"
)

func TestLoopInterval(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		every time.Duration
		loop  bool
	}{
		{"", 0, false},
		{"2m", 2 * time.Minute, true},
		{"5s", 30 * time.Second, true}, // floor
		{"junk", 2 * time.Minute, true},
		{"0", 0, true},
		{"off", 0, true},
	} {
		t.Setenv("OMNI_GUARDIAN_INTERVAL", tc.raw)
		every, loop := loopInterval()
		if every != tc.every || loop != tc.loop {
			t.Errorf("loopInterval(%q) = %s, %v; want %s, %v", tc.raw, every, loop, tc.every, tc.loop)
		}
	}
}

// shims writes PATH-only fakes that log "<name> <argv>" and exit with
// $<NAME>_EXIT (default 0), then points PATH at them alone — so a container
// branch under test can never reach the real pkill.
func shims(t *testing.T, names ...string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "log")
	for _, n := range names {
		script := "#!/bin/sh\necho \"" + n + " $@\" >> " + logPath + "\nexit ${" + strings.ToUpper(n) + "_EXIT:-0}\n"
		if err := os.WriteFile(filepath.Join(dir, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return logPath
}

// flakyStatus answers /status with an error once, then healthy — a server
// that was hung and came back after the restart.
func flakyStatus(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			http.Error(w, "hung", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"app":"omni","version":%q}`, version.Version)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OMNI_ADDR", strings.TrimPrefix(srv.URL, "http://"))
}

func TestCheckServerInContainer(t *testing.T) {
	oldTries, oldDelay := probeTries, probeDelay
	probeTries, probeDelay = 3, time.Millisecond
	t.Cleanup(func() { probeTries, probeDelay = oldTries, oldDelay })

	t.Run("hung", func(t *testing.T) {
		t.Setenv("OMNI_CONTAINER", "1")
		flakyStatus(t)
		logPath := shims(t, "pgrep", "pkill")
		r := checkServer()
		if !r.ok || !strings.Contains(r.event, "active but not responding") {
			t.Fatalf("checkServer = %+v; want healed hung server", r)
		}
		logData, _ := os.ReadFile(logPath)
		for _, want := range []string{"pgrep -x omni-server", "pkill -x omni-server"} {
			if !strings.Contains(string(logData), want) {
				t.Errorf("shim log %q missing %q", logData, want)
			}
		}
	})

	t.Run("dead", func(t *testing.T) {
		t.Setenv("OMNI_CONTAINER", "1")
		t.Setenv("PGREP_EXIT", "1") // no such process: the entrypoint is already respawning
		flakyStatus(t)
		logPath := shims(t, "pgrep", "pkill")
		r := checkServer()
		if !r.ok || !strings.Contains(r.event, "not running") {
			t.Fatalf("checkServer = %+v; want recovered dead server", r)
		}
		if logData, _ := os.ReadFile(logPath); strings.Contains(string(logData), "pkill") {
			t.Fatalf("shim log %q; nothing to kill when the process is gone", logData)
		}
	})

	t.Run("host", func(t *testing.T) {
		t.Setenv("OMNI_CONTAINER", "")
		flakyStatus(t)
		logPath := shims(t, "systemctl", "pgrep", "pkill")
		r := checkServer()
		if !r.ok || !strings.Contains(r.event, "restarted by guardian") {
			t.Fatalf("checkServer = %+v; want systemctl heal", r)
		}
		logData, _ := os.ReadFile(logPath)
		if !strings.Contains(string(logData), "systemctl --user restart omni-server.service") {
			t.Fatalf("shim log %q; want a systemctl restart", logData)
		}
		if strings.Contains(string(logData), "pgrep") || strings.Contains(string(logData), "pkill") {
			t.Fatalf("shim log %q; host mode must not pkill", logData)
		}
	})
}

// TestCheckUpdatesInContainer: the image is the unit of update, so a stale
// omni joins the text alert with the compose hint instead of becoming the
// one-tap offer tag; plugin entries keep their own command.
func TestCheckUpdatesInContainer(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/omni/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v99.9.9"}`)
		case "/repos/johnvilela/pecunia/releases/latest":
			fmt.Fprint(w, `{"tag_name":"v9.9.9"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	t.Setenv("OMNI_GITHUB_API", fake.URL)
	t.Setenv("OMNI_CONTAINER", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pecunia"), []byte("#!/bin/sh\necho 'pecunia 0.1.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := os.MkdirAll(filepath.Join(dataDir(), "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir(), "plugins", "pecunia.json"), []byte(`{"repo":"johnvilela/pecunia"}`), 0o600)

	r, tag, ok := checkUpdates([]string{"owner/omni", "johnvilela/pecunia"})
	if !ok || r.ok || tag != "" {
		t.Fatalf("checkUpdates = %+v, tag %q, %v; want a red text alert and no offer tag", r, tag, ok)
	}
	for _, want := range []string{
		"omni " + version.Version + " → v99.9.9",
		"docker compose pull && docker compose up -d",
		"omni plugins install johnvilela/pecunia",
	} {
		if !strings.Contains(r.detail, want) {
			t.Errorf("detail %q missing %q", r.detail, want)
		}
	}
	if strings.Contains(r.detail, "install.sh") {
		t.Errorf("detail %q names the host installer inside docker", r.detail)
	}

	// ignore is a button concept: in docker the text alert still names omni
	os.WriteFile(filepath.Join(dataDir(), "update.ignore"), []byte("v99.9.9"), 0o600)
	if r, tag, _ := checkUpdates([]string{"owner/omni"}); tag != "" || !strings.Contains(r.detail, "omni ") {
		t.Fatalf("checkUpdates with ignore = %+v, tag %q; want omni still in the text", r, tag)
	}
}
