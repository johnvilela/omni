package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTermSessionRun locks the persistent-shell core over the pipe-REPL shells
// (bash/zsh; fish can't do this and uses the per-command path — see
// TestFishPerCommand): output capture, cwd persistence, the trimmed prompt line,
// and the non-zero exit note.
func TestTermSessionRun(t *testing.T) {
	for _, sh := range []string{"bash", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			t.Logf("skip %s: not installed", sh)
			continue
		}
		t.Run(sh, func(t *testing.T) {
			ts, err := newShell(nil, path)
			if err != nil {
				t.Fatal(err)
			}
			ts.shell = path
			defer ts.close()

			if out, alive := ts.run("echo hi"); !strings.Contains(out, "hi") || !alive {
				t.Fatalf("echo: got %q alive=%v", out, alive)
			}
			// cwd persists (one long-lived process) and shows trimmed in the prompt
			ts.run("cd /tmp")
			if out, _ := ts.run("true"); !strings.Contains(out, "📁 tmp") {
				t.Fatalf("cwd not in prompt: %q", out)
			}
			if out, _ := ts.run("false"); !strings.Contains(out, "[exit 1]") {
				t.Fatalf("exit code not surfaced: %q", out)
			}
			// `exit` kills the shell: alive=false
			if _, alive := ts.run("exit"); alive {
				t.Fatal("shell should be dead after exit")
			}
		})
	}
}

// TestFishPerCommand: fish runs each command as the user in a fresh `fish -c`
// with cwd carried between commands (env/jobs don't persist).
func TestFishPerCommand(t *testing.T) {
	fishPath, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not installed")
	}
	ts := &termSession{shell: fishPath, cwd: "/tmp", nonce: newNonce()}

	if out := ts.runOnce("echo hi"); !strings.Contains(out, "hi") || !strings.Contains(out, "📁 tmp") {
		t.Fatalf("echo: %q", out)
	}
	ts.runOnce("cd /etc") // cwd carries to the next command
	if ts.cwd != "/etc" {
		t.Fatalf("cwd not tracked: %q", ts.cwd)
	}
	if out := ts.runOnce("true"); !strings.Contains(out, "📁 etc") {
		t.Fatalf("carried cwd not in prompt: %q", out)
	}
	if out := ts.runOnce("false"); !strings.Contains(out, "[exit 1]") {
		t.Fatalf("exit code not surfaced: %q", out)
	}
}

func TestAutoSudo(t *testing.T) {
	if got := autoSudo("sudo systemctl restart x"); got != "sudo -A systemctl restart x" {
		t.Fatalf("leading sudo: %q", got)
	}
	if got := autoSudo("ls -la"); got != "ls -la" {
		t.Fatalf("no sudo should pass through: %q", got)
	}
}

// fakeSudo installs a `sudo` on PATH (real bash kept reachable) that accepts
// only the password "pw": it reads the first stdin line and, on a match, execs
// the rest of its args; otherwise exits 1.
func fakeSudo(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
# skip sudo's own flags (-k -S -p '' ...) to the command
while [ $# -gt 0 ]; do
  case "$1" in
    -p) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
read line
[ "$line" = "pw" ] || exit 1
exec "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

// TestApplyPasswordRetry: a wrong password keeps /terminal armed for another
// try (up to maxSudoTries), then gives up; the right one opens the shell.
func TestApplyPasswordRetry(t *testing.T) {
	fakeSudo(t) // accepts "pw"
	s := &Server{}
	p := &termPending{openShell: true}

	for i := 1; i < maxSudoTries; i++ {
		r := s.applyPassword(p, "wrong")
		if s.termPending == nil {
			t.Fatalf("attempt %d dropped out of terminal mode", i)
		}
		if !strings.Contains(r.Text, "try again") || !r.DeleteInbound {
			t.Fatalf("attempt %d: %q", i, r.Text)
		}
	}
	r := s.applyPassword(p, "wrong") // exhausts tries
	if s.termPending != nil || !strings.Contains(r.Text, "too many") {
		t.Fatalf("should give up after %d tries: pending=%v %q", maxSudoTries, s.termPending, r.Text)
	}

	// the right password opens the shell
	p2 := &termPending{openShell: true}
	if r := s.applyPassword(p2, "pw"); s.term == nil || s.termPending != nil || !strings.Contains(r.Text, "terminal mode") {
		t.Fatalf("correct password should open the shell: %q", r.Text)
	}
	s.endTerminal(s.term)
}

func TestValidateSudo(t *testing.T) {
	fakeSudo(t)
	if !validateSudo(context.Background(), "pw") {
		t.Fatal("right password rejected")
	}
	if validateSudo(context.Background(), "nope") {
		t.Fatal("wrong password accepted")
	}
}

// TestRunOneShot: no-sudo runs as-is with no password; a leading sudo is
// rewritten so the piped password authenticates the fake sudo. Every reply
// carries the 📁 prompt line, and the real exit code comes from the sentinel.
func TestRunOneShot(t *testing.T) {
	fakeSudo(t)
	// no-sudo runs in the real login shell: clean output + prompt line
	if out, err := runOneShot(context.Background(), "", "echo plain", defaultShell()); err != nil || !strings.Contains(out, "plain") || !strings.Contains(out, "📁") {
		t.Fatalf("plain: %q %v", out, err)
	}
	// leading-sudo rewrite: pin bash so the fake sudo resolves via PATH (fish
	// rebuilds PATH from its config and would bypass it)
	if out, err := runOneShot(context.Background(), "pw", "sudo echo rooted", "/bin/bash"); err != nil || !strings.Contains(out, "rooted") {
		t.Fatalf("sudo: %q %v", out, err)
	}
	if out, _ := runOneShot(context.Background(), "wrong", "sudo echo x", "/bin/bash"); !strings.Contains(out, "[exit 1]") {
		t.Fatalf("wrong sudo password should surface a non-zero exit: %q", out)
	}
}
