package main

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
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

			if out, _, alive := ts.run("echo hi", io.Discard); !strings.Contains(out, "hi") || !alive {
				t.Fatalf("echo: got %q alive=%v", out, alive)
			}
			// cwd persists (one long-lived process), shows trimmed in the
			// prompt and is reported back for the pin indicator
			ts.run("cd /tmp", io.Discard)
			if out, pwd, _ := ts.run("true", io.Discard); !strings.Contains(out, "📁 tmp") || pwd != "/tmp" {
				t.Fatalf("cwd not in prompt/return: %q %q", out, pwd)
			}
			if out, _, _ := ts.run("false", io.Discard); !strings.Contains(out, "[exit 1]") {
				t.Fatalf("exit code not surfaced: %q", out)
			}
			// `exit` kills the shell: alive=false
			if _, _, alive := ts.run("exit", io.Discard); alive {
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
	bg := context.Background()

	if out, _ := ts.runOnce(bg, "echo hi", io.Discard); !strings.Contains(out, "hi") || !strings.Contains(out, "📁 tmp") {
		t.Fatalf("echo: %q", out)
	}
	ts.runOnce(bg, "cd /etc", io.Discard) // cwd carries to the next command
	if ts.cwd != "/etc" {
		t.Fatalf("cwd not tracked: %q", ts.cwd)
	}
	if out, pwd := ts.runOnce(bg, "true", io.Discard); !strings.Contains(out, "📁 etc") || pwd != "/etc" {
		t.Fatalf("carried cwd not in prompt/return: %q %q", out, pwd)
	}
	if out, _ := ts.runOnce(bg, "false", io.Discard); !strings.Contains(out, "[exit 1]") {
		t.Fatalf("exit code not surfaced: %q", out)
	}
}

// TestRunOneShot: a plain command runs in the login shell with the 📁 prompt
// line; the exit code comes from the sentinel; the child never sees the
// server's secrets.
func TestRunOneShot(t *testing.T) {
	if out, err := runOneShot(context.Background(), "echo plain", defaultShell(), io.Discard); err != nil || !strings.Contains(out, "plain") || !strings.Contains(out, "📁") {
		t.Fatalf("plain: %q %v", out, err)
	}
	if out, _ := runOneShot(context.Background(), "false", "/bin/bash", io.Discard); !strings.Contains(out, "[exit 1]") {
		t.Fatalf("exit code not surfaced: %q", out)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "AUDIT_BOT_SECRET")
	if out, _ := runOneShot(context.Background(), "echo [$TELEGRAM_BOT_TOKEN] $OMNI_ASKPASS_PW $SUDO_ASKPASS", "/bin/bash", io.Discard); strings.Contains(out, "AUDIT_BOT_SECRET") || strings.Contains(out, "ASKPASS") {
		t.Fatalf("child inherited a secret: %q", out)
	}
}

// TestTerminalRefusesSudo: no password prompt anywhere — /terminal opens an
// unprivileged shell straight away and sudo is refused in both entry points.
func TestTerminalRefusesSudo(t *testing.T) {
	s := &Server{}
	if r, ok := s.handleTerminal(context.Background(), "$ sudo ls"); !ok || !strings.Contains(r.Text, "sudo is disabled") {
		t.Fatalf("one-shot sudo = %v %q", ok, r.Text)
	}
	r, ok := s.handleTerminal(context.Background(), "/terminal")
	if !ok || strings.Contains(r.Text, "password") || s.term == nil {
		t.Fatalf("/terminal = %v %q term=%v", ok, r.Text, s.term)
	}
	defer s.endTerminal(s.term)
	if r, _ := s.handleTerminal(context.Background(), "sudo -i"); !strings.Contains(r.Text, "sudo is disabled") {
		t.Fatalf("in-session sudo = %q", r.Text)
	}
}

// TestInterruptPersistentShell locks in the trap+Setpgid design: a group
// SIGINT kills the running command ([exit 130]) while the shell survives to
// run the next one.
func TestInterruptPersistentShell(t *testing.T) {
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

			type res struct {
				out   string
				alive bool
			}
			ch := make(chan res, 1)
			go func() {
				out, _, alive := ts.run("sleep 30", io.Discard)
				ch <- res{out, alive}
			}()
			time.Sleep(300 * time.Millisecond)
			if err := syscall.Kill(-ts.cmd.Process.Pid, syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-ch:
				if !r.alive || !strings.Contains(r.out, "[exit 130]") {
					t.Fatalf("interrupted run: %q alive=%v", r.out, r.alive)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("SIGINT did not unblock the command")
			}
			if out, _, alive := ts.run("echo ok", io.Discard); !alive || !strings.Contains(out, "ok") {
				t.Fatalf("shell did not survive the interrupt: %q alive=%v", out, alive)
			}
		})
	}
}

// TestInterruptOneShot: cancelling the ctx group-SIGINTs a "$" one-shot out of
// its sleep (c.Cancel + WaitDelay path).
func TestInterruptOneShot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runOneShot(ctx, "sleep 30", "/bin/bash", io.Discard)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not unblock runOneShot")
	}
}

func TestHandleInterrupt(t *testing.T) {
	s := &Server{}
	if r := s.handleInterrupt(); r.Text != "nothing running" {
		t.Fatalf("idle: %q", r.Text)
	}
	called := false
	s.oneShotCancel = func() { called = true }
	if r := s.handleInterrupt(); !strings.Contains(r.Text, "⏹") || !called {
		t.Fatalf("one-shot: %q called=%v", r.Text, called)
	}
	s.oneShotCancel = nil
	s.term = &termSession{}
	if r := s.handleInterrupt(); r.Text != "nothing running" {
		t.Fatalf("terminal idle: %q", r.Text)
	}
	called = false
	s.term = &termSession{busy: true, cancel: func() { called = true }}
	if r := s.handleInterrupt(); !strings.Contains(r.Text, "⏹") || !called {
		t.Fatalf("fish busy: %q called=%v", r.Text, called)
	}
}

func TestTail(t *testing.T) {
	if got := tail("hello", 10); got != "hello" {
		t.Fatalf("short: %q", got)
	}
	if got := tail("aaaa📁bb", 6); got != "📁bb" {
		t.Fatalf("exact fit: %q", got)
	}
	if got := tail("aaaa📁bb", 5); got != "bb" { // never split a rune: drop it
		t.Fatalf("rune boundary: %q", got)
	}
}

// TestTerminalIdleTTL: an idle session auto-closes with a notice; a busy one
// survives its timer firing.
func TestTerminalIdleTTL(t *testing.T) {
	old := termIdleTTL
	termIdleTTL = 50 * time.Millisecond
	t.Cleanup(func() { termIdleTTL = old })
	srv, _, calls := newPinServer(t)

	if r := srv.openTerminal(); !strings.Contains(r.Text, "terminal mode") {
		t.Fatalf("shell did not open: %q", r.Text)
	}
	sent := nextCall(t, calls, "sendMessage")
	if text, _ := sent["text"].(string); !strings.HasPrefix(text, "🖥 terminal closed — idle") {
		t.Fatalf("idle notice = %v", sent)
	}
	srv.termMu.Lock()
	gone := srv.term == nil
	srv.termMu.Unlock()
	if !gone {
		t.Fatal("session should be torn down after the TTL")
	}

	// busy: the timer firing must not kill the session
	ts := &termSession{busy: true}
	srv.termMu.Lock()
	srv.term = ts
	srv.termMu.Unlock()
	srv.expireTerminal(ts)
	srv.termMu.Lock()
	kept := srv.term == ts
	srv.termMu.Unlock()
	if !kept {
		t.Fatal("busy session must survive expiry")
	}
	noCall(t, calls)
}

// TestStreamProgress drives the progress loop against the recording fake:
// send → edit → final edit-in-place; a too-long final is delete + chunked
// send; a fast command never streams at all.
func TestStreamProgress(t *testing.T) {
	oldAfter, oldEvery := streamAfter, streamEvery
	streamAfter, streamEvery = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { streamAfter, streamEvery = oldAfter, oldEvery })
	srv, _, calls := newPinServer(t)

	buf := &streamBuf{}
	buf.Write([]byte("building server...\n"))
	finish := srv.streamProgress(buf)
	sent := nextCall(t, calls, "sendMessage")
	if text, _ := sent["text"].(string); !strings.Contains(text, "🖥 running ·") || !strings.Contains(text, "building server...") {
		t.Fatalf("progress = %v", sent)
	}
	finish("final result")
	// intermediate progress edits may still land; the final edit is last
	if last := lastCall(t, calls); last == nil || last["method"] != "editMessageText" || last["text"] != "final result" {
		t.Fatalf("final delivery = %v", last)
	}

	// a >4096 final can't be edited in: delete + chunked sends
	finish = srv.streamProgress(&streamBuf{})
	nextCall(t, calls, "sendMessage")
	finish(strings.Repeat("x", 5000))
	deadline := time.After(3 * time.Second)
	var deletes, sends int
	for deletes == 0 || sends < 2 {
		select {
		case c := <-calls:
			switch c["method"] {
			case "deleteMessage":
				deletes++
			case "sendMessage":
				sends++
			}
		case <-deadline:
			t.Fatalf("long final: deletes=%d sends=%d", deletes, sends)
		}
	}

	// fast command: finish before streamAfter → one plain message, no edits
	streamAfter = time.Hour
	finish = srv.streamProgress(&streamBuf{})
	finish("quick")
	if sent := nextCall(t, calls, "sendMessage"); sent["text"] != "quick" {
		t.Fatalf("fast path = %v", sent)
	}
	noCall(t, calls)
}

// lastCall drains calls until they go quiet and returns the final one.
func lastCall(t *testing.T, calls <-chan map[string]any) map[string]any {
	t.Helper()
	var last map[string]any
	for {
		select {
		case c := <-calls:
			last = c
		case <-time.After(300 * time.Millisecond):
			return last
		}
	}
}

// TestPinTerminalIndicator: the dashboard leads with the 🖥 line while a
// terminal session is active.
func TestPinTerminalIndicator(t *testing.T) {
	srv, store, calls := newPinServer(t)
	if err := store.SetPin(42, 100, "clean"); err != nil {
		t.Fatal(err)
	}
	srv.termMu.Lock()
	srv.term = &termSession{}
	srv.termCwd = "/home/x/dev/omni"
	srv.termMu.Unlock()
	srv.refreshPin()
	edit := nextCall(t, calls, "editMessageText")
	if text, _ := edit["text"].(string); !strings.HasPrefix(text, "🖥 TERMINAL · dev/omni\n▶ —") {
		t.Fatalf("pin text = %q", text)
	}
}
