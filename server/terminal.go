package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// oneShotTimeout bounds a "$" command; the persistent shell has no per-command
// timeout — /interrupt (^C) or /exit handle a wedged command instead (both run
// on the poller, not the blocked worker).
const oneShotTimeout = 5 * time.Minute

// Idle TTL and streaming cadence (vars so tests can shrink them).
var (
	termIdleTTL = 5 * time.Minute // /terminal auto-closes after this much idle
	streamAfter = 5 * time.Second // progress message appears once a command runs this long
	streamEvery = 3 * time.Second // progress edit cadence
)

// sudoWord decides whether a "$" one-shot needs the sudo password prompt.
var sudoWord = regexp.MustCompile(`\bsudo\b`)

// termSession drives /terminal. Commands run as the service user (so the user's
// own shell config — fish functions, zoxide, mise, aliases — loads); `sudo` is
// auto-authenticated via SUDO_ASKPASS with the cached password. For bash/zsh
// it's one long-lived `<shell>` process (cmd/stdin/out set), so cd/env/jobs
// persist. Fish can't be fed line-by-line over a pipe (it reads to EOF before
// executing and needs a PTY to run interactively), so a fish session runs each
// command as a fresh `fish -c` (cmd nil): only cwd persists (tracked here). The
// validated password is held in memory for the session so it's asked just once.
type termSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	cmds   chan string
	nonce  string
	shell  string // login shell path, selects the sentinel syntax
	cwd    string // fish mode: tracked working dir (cmd nil)
	pass   string // sudo password for SUDO_ASKPASS, memory only, cleared on teardown
	killed bool   // set by /exit so the worker stays quiet on teardown

	busy   bool               // a command is in flight (guarded by Server.termMu)
	cancel context.CancelFunc // ^C for the in-flight fish command (termMu; nil for bash/zsh)
	idle   *time.Timer        // idle-TTL auto-close, armed by startTerminal
}

// maxSudoTries matches sudo's default: wrong-password attempts before giving up.
const maxSudoTries = 3

// termPending remembers what to do once the next message delivers the sudo
// password: open the root shell, or run one parked "$" one-shot. tries counts
// wrong attempts so /terminal re-prompts like a terminal does.
type termPending struct {
	openShell bool
	oneShot   string
	tries     int
}

// handleTerminal is the first thing handleMessage calls. ok=false falls through
// to normal command/LLM handling; ok=true means this owns the message. In
// terminal mode every message except /exit and /interrupt is a shell command.
func (s *Server) handleTerminal(ctx context.Context, text string) (tgReply, bool) {
	s.termMu.Lock()
	pending, term := s.termPending, s.term
	s.termMu.Unlock()

	// awaiting the sudo password — this message is it
	if pending != nil {
		if text == "/exit" {
			s.termMu.Lock()
			s.termPending = nil
			s.termMu.Unlock()
			return tgReply{Text: "cancelled"}, true
		}
		return s.applyPassword(pending, text), true
	}

	// inside terminal mode
	if term != nil {
		switch text {
		case "/exit":
			s.endTerminal(term)
			go s.refreshPin()
			return tgReply{Text: "left terminal mode"}, true
		case "/interrupt":
			return s.handleInterrupt(), true
		}
		// the send happens under termMu and only while this session is still
		// current, so it can't race the idle TTL closing the channel
		s.termMu.Lock()
		live, queued := s.term == term, false
		if live {
			select {
			case term.cmds <- text:
				queued = true
			default:
			}
		}
		s.termMu.Unlock()
		switch {
		case !live:
			return tgReply{Text: "🖥 terminal closed"}, true // expired since the snapshot above
		case !queued:
			return tgReply{Text: "⏳ command queue full — /interrupt to cancel, /exit to abort"}, true
		}
		term.idle.Reset(termIdleTTL)
		return tgReply{}, true // output arrives async via notifyOwner
	}

	// "$ cmd" one-shot
	if rest, ok := strings.CutPrefix(text, "$"); ok {
		cmd := strings.TrimSpace(rest)
		if cmd == "" {
			return tgReply{Text: "usage: $ <command>"}, true
		}
		if sudoWord.MatchString(cmd) {
			s.termMu.Lock()
			s.termPending = &termPending{oneShot: cmd}
			s.termMu.Unlock()
			return tgReply{Text: "🔒 sudo password:"}, true
		}
		go s.deliverOneShot(context.Background(), "", cmd)
		return tgReply{}, true
	}

	// enter terminal mode
	if text == "/terminal" {
		s.termMu.Lock()
		s.termPending = &termPending{openShell: true}
		s.termMu.Unlock()
		return tgReply{Text: "🔒 sudo password to start terminal mode (/exit to cancel):"}, true
	}

	return tgReply{}, false
}

// applyPassword consumes the password message: validates and opens the root
// shell, or runs the parked one-shot. A wrong password for /terminal stays armed
// for another attempt (up to maxSudoTries) instead of dropping the mode. The
// password is used here and discarded — never stored. DeleteInbound removes the
// password message from the chat.
func (s *Server) applyPassword(p *termPending, pass string) tgReply {
	if p.openShell && !validateSudo(context.Background(), pass) {
		p.tries++
		if p.tries >= maxSudoTries {
			s.setPending(nil)
			return tgReply{Text: "⚠ wrong sudo password — too many attempts, cancelled", DeleteInbound: true}
		}
		s.setPending(p) // stay armed: the next message is another attempt
		return tgReply{Text: fmt.Sprintf("⚠ wrong sudo password — try again (%d left, /exit to cancel)", maxSudoTries-p.tries), DeleteInbound: true}
	}
	s.setPending(nil)

	if p.openShell {
		ts, err := s.startTerminal(pass)
		if err != nil {
			return tgReply{Text: "⚠ " + err.Error(), DeleteInbound: true}
		}
		s.termMu.Lock()
		s.term = ts
		s.termMu.Unlock()
		go s.refreshPin()
		return tgReply{Text: "🖥 terminal mode — running as you; `sudo` is auto-authenticated. /exit to leave.", DeleteInbound: true}
	}
	go s.deliverOneShot(context.Background(), pass, p.oneShot)
	return tgReply{DeleteInbound: true}
}

// setPending swaps the awaiting-password state under the lock.
func (s *Server) setPending(p *termPending) {
	s.termMu.Lock()
	s.termPending = p
	s.termMu.Unlock()
}

// deliverOneShot runs a "$" command and pushes the output to the owner chats,
// streaming progress while it runs. The cancel is published so /interrupt can
// ^C it. ponytail: concurrent $ one-shots — last one wins the stored cancel.
func (s *Server) deliverOneShot(ctx context.Context, pass, cmd string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.termMu.Lock()
	s.oneShotCancel = cancel
	s.termMu.Unlock()
	defer func() {
		s.termMu.Lock()
		s.oneShotCancel = nil
		s.termMu.Unlock()
	}()
	stop := s.typingOwner(ctx)
	buf := &streamBuf{}
	finish := s.streamProgress(buf)
	out, err := runOneShot(ctx, pass, cmd, defaultShell(), buf)
	stop()
	if err != nil && strings.TrimSpace(out) == "" {
		out = "⚠ " + err.Error()
	}
	if strings.TrimSpace(out) == "" {
		out = "✓ (no output)"
	}
	finish(out)
}

// runOneShot runs one command via bash and returns the same prompt-prefixed
// reply as the persistent shell (📁 cwd (branch), plus output and [exit N]). A
// leading `sudo` is rewritten to read the piped password; a mid-pipeline sudo is
// left as-is (it fails with a clear tty error — use /terminal for those).
func runOneShot(ctx context.Context, pass, cmd, shell string, w io.Writer) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, oneShotTimeout)
	defer cancel()
	if pass != "" && strings.HasPrefix(cmd, "sudo ") {
		cmd = "sudo -k -S -p '' " + strings.TrimPrefix(cmd, "sudo ")
	}
	nonce := newNonce()
	full := cmd + "\n" + shellProbe(shell, nonce) + "\n"
	c := exec.CommandContext(ctx, shell, "-c", full)
	if pass != "" {
		c.Stdin = strings.NewReader(pass + "\n")
	}
	var raw bytes.Buffer
	mw := io.MultiWriter(&raw, w)
	c.Stdout, c.Stderr = mw, mw
	groupInterrupt(c)
	err := c.Run()
	body, sentinel, ok := strings.Cut(raw.String(), nonce)
	if !ok {
		return strings.TrimRight(body, "\n"), err // probe never ran (interrupt/timeout/exec error)
	}
	return formatShellReply(sentinel, body), nil
}

// groupInterrupt puts the command in its own process group and makes ctx
// cancellation (/interrupt, timeout) a group SIGINT, so the whole child tree
// dies like a ^C. WaitDelay is load-bearing: a backgrounded grandchild holding
// the merged output pipe would otherwise hang Wait forever.
func groupInterrupt(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGINT) }
	c.WaitDelay = 10 * time.Second
}

// validateSudo checks the password before opening the persistent shell, so a
// wrong one is rejected cleanly instead of corrupting the command stream.
func validateSudo(ctx context.Context, pass string) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "sudo", "-k", "-S", "-p", "", "true")
	c.Stdin = strings.NewReader(pass + "\n")
	return c.Run() == nil
}

// startTerminal opens a /terminal session on the user's login shell, running as
// the service user (password already validated, cached for sudo -A). bash/zsh
// get a persistent process; fish gets a per-command session (see termSession).
// Either way one worker drains it.
func (s *Server) startTerminal(pass string) (*termSession, error) {
	shell := defaultShell()
	var ts *termSession
	if filepath.Base(shell) == "fish" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "/"
		}
		ts = &termSession{shell: shell, cwd: cwd, pass: pass, cmds: make(chan string, 64), nonce: newNonce()}
	} else {
		var err error
		ts, err = newShell(sudoEnv(pass), shell)
		if err != nil {
			return nil, err
		}
		ts.shell = shell
		ts.pass = pass
	}
	ts.idle = time.AfterFunc(termIdleTTL, func() { s.expireTerminal(ts) })
	cwd := ts.cwd
	if cwd == "" {
		cwd, _ = os.Getwd() // bash/zsh report theirs with the first sentinel
	}
	s.termMu.Lock()
	s.termCwd = cwd
	s.termMu.Unlock()
	go s.termWorker(ts)
	return ts, nil
}

// askpass caches a tiny SUDO_ASKPASS helper that echoes $OMNI_ASKPASS_PW, so
// `sudo -A` gets the password with no tty and no password on disk (it lives only
// in the shell process's environment).
var (
	askpassOnce sync.Once
	askpassPath string
	askpassErr  error
)

func askpassScript() (string, error) {
	askpassOnce.Do(func() {
		askpassPath = filepath.Join(os.TempDir(), "omni-askpass.sh")
		askpassErr = os.WriteFile(askpassPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$OMNI_ASKPASS_PW\"\n"), 0o700)
	})
	return askpassPath, askpassErr
}

// sudoEnv is the process env for a terminal command run as the user: the normal
// environment plus the askpass wiring that feeds the cached password to sudo -A.
func sudoEnv(pass string) []string {
	env := os.Environ()
	if s, err := askpassScript(); err == nil {
		env = append(env, "SUDO_ASKPASS="+s, "OMNI_ASKPASS_PW="+pass)
	}
	return env
}

// autoSudo turns a leading `sudo` into `sudo -A` so it authenticates via the
// askpass helper (cached password) instead of prompting on a tty we don't have.
func autoSudo(cmd string) string {
	if strings.HasPrefix(cmd, "sudo ") {
		return "sudo -A " + strings.TrimPrefix(cmd, "sudo ")
	}
	return cmd
}

// defaultShell resolves the service user's login shell (fish/bash/zsh/…) so
// /terminal and "$" behave like the user's own shell. getent is authoritative;
// $SHELL is a fallback (it reflects the launching process, not the login shell).
func defaultShell() string {
	if out, err := exec.Command("getent", "passwd", strconv.Itoa(os.Getuid())).Output(); err == nil {
		if f := strings.Split(strings.TrimSpace(string(out)), ":"); len(f) >= 7 && f[6] != "" {
			return f[6]
		}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/bash"
}

// shellProbe is the sentinel command appended after a user command to report
// exit code, cwd and git branch. fish needs its own syntax ($status, (…));
// bash/zsh/sh share the POSIX form ($?, "$(…)").
func shellProbe(shell, nonce string) string {
	if filepath.Base(shell) == "fish" {
		return "set __c $status; printf '" + nonce + "%s\\t%s\\t%s\\n' \"$__c\" \"$PWD\" (git branch --show-current 2>/dev/null)"
	}
	return "__c=$?; printf '" + nonce + "%s\\t%s\\t%s\\n' \"$__c\" \"$PWD\" \"$(git branch --show-current 2>/dev/null)\""
}

// newShell starts argv with a stdin pipe and stdout+stderr merged into one pipe
// we read line by line. The parent drops its write end so reads see EOF when the
// shell exits.
func newShell(env []string, argv ...string) (*termSession, error) {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = env // nil inherits the parent environment
	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	c.Stdout, c.Stderr = pw, pw
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		stdin.Close()
		return nil, err
	}
	pw.Close()
	// a handler (unlike trap '') is reset to default in children, so
	// /interrupt's group SIGINT kills the running command but not the shell.
	// ponytail: builtin-only loops shrug off SIGINT (no child) — /exit is the hammer.
	io.WriteString(stdin, "trap ':' INT\n")
	return &termSession{cmd: c, stdin: stdin, out: bufio.NewReader(pr), cmds: make(chan string, 64), nonce: newNonce()}, nil
}

// termWorker runs the session's commands in order and pushes each result. Only
// this goroutine touches stdin/out, so runs stay serialized.
func (s *Server) termWorker(ts *termSession) {
	for cmd := range ts.cmds {
		ctx, cancel := context.WithCancel(context.Background())
		s.termMu.Lock()
		ts.busy, ts.cancel = true, cancel
		s.termMu.Unlock()
		stop := s.typingOwner(ctx)
		buf := &streamBuf{}
		finish := s.streamProgress(buf)
		out, pwd, alive := ts.exec(ctx, cmd, buf)
		stop()
		cancel()
		s.termMu.Lock()
		ts.busy, ts.cancel = false, nil
		if pwd != "" {
			s.termCwd = pwd
		}
		s.termMu.Unlock()
		ts.idle.Reset(termIdleTTL)
		if strings.TrimSpace(out) == "" {
			out = "✓ (no output)"
		}
		finish(out)
		go s.refreshPin() // the 🖥 cwd segment may have changed
		if !alive {
			break
		}
	}
	if ts.stdin != nil {
		ts.stdin.Close()
	}
	if ts.cmd != nil {
		ts.cmd.Wait()
	}
	if ts.idle != nil {
		ts.idle.Stop()
	}
	ts.pass = "" // drop the cached fish password
	s.termMu.Lock()
	if s.term == ts {
		s.term = nil
	}
	s.termMu.Unlock()
	go s.refreshPin()
	if !ts.killed {
		s.notifyOwner(context.Background(), tgReply{Text: "🖥 terminal session ended"})
	}
}

// exec runs one command in the session: the persistent shell (bash/zsh) or a
// fresh `sudo fish -c` (fish). alive=false ends the session; pwd is the cwd
// the sentinel reported ("" when the shell died). w sees the output live.
func (ts *termSession) exec(ctx context.Context, cmd string, w io.Writer) (out, pwd string, alive bool) {
	if ts.cmd != nil {
		return ts.run(cmd, w)
	}
	out, pwd = ts.runOnce(ctx, cmd, w)
	return out, pwd, true
}

// runOnce runs one fish command as the user in a fresh `fish -c` (fish can't be
// a persistent piped shell). cwd is carried between commands; env vars and
// background jobs do not persist. `sudo` is auto-authenticated via SUDO_ASKPASS.
func (ts *termSession) runOnce(ctx context.Context, cmd string, w io.Writer) (out, pwd string) {
	ctx, cancel := context.WithTimeout(ctx, oneShotTimeout)
	defer cancel()
	full := autoSudo(cmd) + "\n" + shellProbe(ts.shell, ts.nonce)
	c := exec.CommandContext(ctx, ts.shell, "-c", full)
	c.Dir = ts.cwd
	c.Env = sudoEnv(ts.pass)
	var raw bytes.Buffer
	mw := io.MultiWriter(&raw, w)
	c.Stdout, c.Stderr = mw, mw
	groupInterrupt(c)
	err := c.Run()
	body, sentinel, ok := strings.Cut(raw.String(), ts.nonce)
	if !ok {
		if out := strings.TrimRight(body, "\n"); out != "" || err == nil {
			return out, ""
		}
		return "⚠ " + err.Error(), ""
	}
	if _, p, _ := parseSentinel(sentinel); p != "" {
		ts.cwd = p // carry cwd to the next command
	}
	return formatShellReply(sentinel, body), ts.cwd
}

// run writes one command plus a sentinel that reports the exit code, cwd and
// git branch, then reads merged output up to that line. The reply is prefixed
// with a prompt line (📁 dev/omni (branch)) so the user can locate themselves.
// alive=false means the shell died (EOF before the sentinel, e.g. `exit`).
func (ts *termSession) run(cmd string, w io.Writer) (out, pwd string, alive bool) {
	probe := autoSudo(cmd) + "\n" + shellProbe(ts.shell, ts.nonce) + "\n"
	if _, err := io.WriteString(ts.stdin, probe); err != nil {
		return "⚠ shell write failed: " + err.Error(), "", false
	}
	var b strings.Builder
	for {
		line, err := ts.out.ReadString('\n')
		if rest, ok := strings.CutPrefix(line, ts.nonce); ok {
			_, pwd, _ = parseSentinel(rest)
			return formatShellReply(rest, b.String()), pwd, true
		}
		b.WriteString(line)
		io.WriteString(w, line)
		if err != nil {
			return strings.TrimRight(b.String(), "\n"), "", false
		}
	}
}

// parseSentinel splits the sentinel tail into exit code, cwd and git branch.
func parseSentinel(sentinel string) (code, pwd, branch string) {
	f := strings.SplitN(strings.TrimRight(sentinel, "\n"), "\t", 3)
	code = f[0]
	if len(f) > 1 {
		pwd = f[1]
	}
	if len(f) > 2 {
		branch = f[2]
	}
	return code, pwd, branch
}

// formatShellReply builds the prompt line from the sentinel tail
// (code\tpwd\tbranch) and prepends it to the command's output.
func formatShellReply(sentinel, body string) string {
	code, pwd, branch := parseSentinel(sentinel)
	prompt := "📁 " + shortPath(pwd)
	if branch != "" {
		prompt += " (" + branch + ")"
	}
	out := prompt
	if body = strings.TrimRight(body, "\n"); body != "" {
		out += "\n\n" + body
	}
	if n, _ := strconv.Atoi(code); n != 0 {
		out += fmt.Sprintf("\n[exit %d]", n)
	}
	return out
}

// endTerminal (from /exit or the idle TTL) kills the shell and unblocks the
// worker. Fully under termMu so it can't race the poller's channel send or a
// concurrent expiry; s.term == ts is the not-already-torn-down guard.
func (s *Server) endTerminal(ts *termSession) bool {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	return s.endTerminalLocked(ts)
}

func (s *Server) endTerminalLocked(ts *termSession) bool {
	if s.term != ts {
		return false
	}
	s.term = nil
	ts.killed = true
	if ts.idle != nil {
		ts.idle.Stop()
	}
	if ts.cmd != nil && ts.cmd.Process != nil {
		ts.cmd.Process.Kill()
	}
	if ts.cancel != nil {
		ts.cancel() // abort an in-flight fish command too
	}
	close(ts.cmds)
	return true
}

// expireTerminal is the idle-TTL callback: tear down only if the session is
// still current and idle — a busy command's completion re-arms the timer.
func (s *Server) expireTerminal(ts *termSession) {
	s.termMu.Lock()
	ended := !ts.busy && s.endTerminalLocked(ts)
	s.termMu.Unlock()
	if !ended {
		return
	}
	s.notifyOwner(context.Background(), tgReply{Text: "🖥 terminal closed — idle " + termIdleTTL.String()})
	go s.refreshPin()
}

// handleInterrupt is ^C for the foreground command: a group SIGINT for the
// persistent shell (its INT trap keeps the shell itself alive), the ctx cancel
// for an in-flight fish command, or the running "$" one-shot's cancel.
func (s *Server) handleInterrupt() tgReply {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if ts := s.term; ts != nil {
		if !ts.busy {
			return tgReply{Text: "nothing running"}
		}
		if ts.cmd != nil {
			syscall.Kill(-ts.cmd.Process.Pid, syscall.SIGINT)
		} else if ts.cancel != nil {
			ts.cancel()
		}
		return tgReply{Text: "⏹ sent ^C"}
	}
	if s.oneShotCancel != nil {
		s.oneShotCancel()
		return tgReply{Text: "⏹ sent ^C"}
	}
	return tgReply{Text: "nothing running"}
}

// close tears down a shell started outside the worker (tests).
func (ts *termSession) close() {
	ts.stdin.Close()
	if ts.cmd.Process != nil {
		ts.cmd.Process.Kill()
	}
	ts.cmd.Wait()
}

// shortPath keeps only the last two path segments (…/dev/omni → dev/omni).
func shortPath(p string) string {
	segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	if len(segs) == 0 {
		return "/"
	}
	if len(segs) > 2 {
		segs = segs[len(segs)-2:]
	}
	return strings.Join(segs, "/")
}

func newNonce() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "__OMNI_" + hex.EncodeToString(b) + "__"
}

// streamBuf collects merged live output for the progress message.
type streamBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (sb *streamBuf) Write(p []byte) (int, error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.b.Write(p)
}

func (sb *streamBuf) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.b.String()
}

// tail keeps the last max bytes of s, cut at a rune boundary.
func tail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// streamProgress starts the live progress loop for one command: once it has
// run streamAfter, one "🖥 running · Ns" message per owner chat, edited every
// streamEvery with elapsed time and the output tail. The returned finish
// delivers the final reply: edited in place when it fits one message, else the
// progress message is deleted and the reply sent normally (chunked). ponytail:
// edits are best-effort with no rate limiting — one edit every few seconds is
// well inside telegram's tolerance.
func (s *Server) streamProgress(buf *streamBuf) (finish func(final string)) {
	done, idsCh, start := make(chan struct{}), make(chan map[int64]int64, 1), time.Now()
	go func() {
		ids := map[int64]int64{}
		defer func() { idsCh <- ids }()
		select {
		case <-done:
			return
		case <-time.After(streamAfter):
		}
		for {
			s.mu.Lock()
			tg := s.tg
			s.mu.Unlock()
			if tg != nil {
				text := fmt.Sprintf("🖥 running · %ds", int(time.Since(start).Seconds()))
				if t := strings.TrimRight(tail(buf.String(), 3500), "\n"); t != "" {
					text += "\n\n" + t
				}
				for _, chat := range s.ownerChats() {
					if id, ok := ids[chat]; ok {
						tg.editMessage(context.Background(), chat, id, text, nil) // best-effort
					} else if id, err := tg.sendReturningID(context.Background(), chat, text); err == nil {
						ids[chat] = id
					}
				}
			}
			select {
			case <-done:
				return
			case <-time.After(streamEvery):
			}
		}
	}()
	return func(final string) {
		close(done)
		ids := <-idsCh
		s.mu.Lock()
		tg := s.tg
		s.mu.Unlock()
		if tg == nil || len(ids) == 0 {
			s.notifyOwner(context.Background(), tgReply{Text: final})
			return
		}
		for _, chat := range s.ownerChats() {
			id, ok := ids[chat]
			if ok && len(final) <= 4096 && tg.editMessage(context.Background(), chat, id, final, nil) == nil {
				continue
			}
			if ok {
				tg.deleteMessage(context.Background(), chat, id) // final too long, or the edit failed
			}
			tg.send(context.Background(), chat, tgReply{Text: final})
		}
	}
}
