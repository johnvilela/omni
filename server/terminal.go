package main

import (
	"bufio"
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
	"time"
)

// oneShotTimeout bounds a "$" command; the persistent shell has no per-command
// timeout — /exit force-kills a wedged command instead (it's handled on the
// poller, not the blocked worker).
const oneShotTimeout = 5 * time.Minute

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
// terminal mode every message except /exit is a shell command.
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
		if text == "/exit" {
			s.endTerminal(term)
			return tgReply{Text: "left terminal mode"}, true
		}
		select {
		case term.cmds <- text:
			return tgReply{}, true // output arrives async via notifyOwner
		default:
			return tgReply{Text: "⏳ a command is still running — /exit to abort"}, true
		}
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

// deliverOneShot runs a "$" command and pushes the output to the owner chats.
func (s *Server) deliverOneShot(ctx context.Context, pass, cmd string) {
	stop := s.typingOwner(ctx)
	out, err := runOneShot(ctx, pass, cmd, defaultShell())
	stop()
	if err != nil && strings.TrimSpace(out) == "" {
		out = "⚠ " + err.Error()
	}
	if strings.TrimSpace(out) == "" {
		out = "✓ (no output)"
	}
	s.notifyOwner(ctx, tgReply{Text: out})
}

// runOneShot runs one command via bash and returns the same prompt-prefixed
// reply as the persistent shell (📁 cwd (branch), plus output and [exit N]). A
// leading `sudo` is rewritten to read the piped password; a mid-pipeline sudo is
// left as-is (it fails with a clear tty error — use /terminal for those).
func runOneShot(ctx context.Context, pass, cmd, shell string) (string, error) {
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
	raw, err := c.CombinedOutput()
	body, sentinel, ok := strings.Cut(string(raw), nonce)
	if !ok {
		return strings.TrimRight(string(raw), "\n"), err // probe never ran (timeout/exec error)
	}
	return formatShellReply(sentinel, body), nil
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
	if filepath.Base(shell) == "fish" {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "/"
		}
		ts := &termSession{shell: shell, cwd: cwd, pass: pass, cmds: make(chan string, 64), nonce: newNonce()}
		go s.termWorker(ts)
		return ts, nil
	}
	ts, err := newShell(sudoEnv(pass), shell)
	if err != nil {
		return nil, err
	}
	ts.shell = shell
	ts.pass = pass
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
	if err := c.Start(); err != nil {
		pr.Close()
		pw.Close()
		stdin.Close()
		return nil, err
	}
	pw.Close()
	return &termSession{cmd: c, stdin: stdin, out: bufio.NewReader(pr), cmds: make(chan string, 64), nonce: newNonce()}, nil
}

// termWorker runs the session's commands in order and pushes each result. Only
// this goroutine touches stdin/out, so runs stay serialized.
func (s *Server) termWorker(ts *termSession) {
	for cmd := range ts.cmds {
		stop := s.typingOwner(context.Background())
		out, alive := ts.exec(cmd)
		stop()
		if strings.TrimSpace(out) == "" {
			out = "✓ (no output)"
		}
		s.notifyOwner(context.Background(), tgReply{Text: out})
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
	ts.pass = "" // drop the cached fish password
	s.termMu.Lock()
	if s.term == ts {
		s.term = nil
	}
	s.termMu.Unlock()
	if !ts.killed {
		s.notifyOwner(context.Background(), tgReply{Text: "🖥 terminal session ended"})
	}
}

// exec runs one command in the session: the persistent shell (bash/zsh) or a
// fresh `sudo fish -c` (fish). alive=false ends the session.
func (ts *termSession) exec(cmd string) (out string, alive bool) {
	if ts.cmd != nil {
		return ts.run(cmd)
	}
	return ts.runOnce(cmd), true
}

// runOnce runs one fish command as the user in a fresh `fish -c` (fish can't be
// a persistent piped shell). cwd is carried between commands; env vars and
// background jobs do not persist. `sudo` is auto-authenticated via SUDO_ASKPASS.
func (ts *termSession) runOnce(cmd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), oneShotTimeout)
	defer cancel()
	full := autoSudo(cmd) + "\n" + shellProbe(ts.shell, ts.nonce)
	c := exec.CommandContext(ctx, ts.shell, "-c", full)
	c.Dir = ts.cwd
	c.Env = sudoEnv(ts.pass)
	raw, err := c.CombinedOutput()
	body, sentinel, ok := strings.Cut(string(raw), ts.nonce)
	if !ok {
		if out := strings.TrimRight(string(raw), "\n"); out != "" || err == nil {
			return out
		}
		return "⚠ " + err.Error()
	}
	if _, pwd, _ := parseSentinel(sentinel); pwd != "" {
		ts.cwd = pwd // carry cwd to the next command
	}
	return formatShellReply(sentinel, body)
}

// run writes one command plus a sentinel that reports the exit code, cwd and
// git branch, then reads merged output up to that line. The reply is prefixed
// with a prompt line (📁 dev/omni (branch)) so the user can locate themselves.
// alive=false means the shell died (EOF before the sentinel, e.g. `exit`).
func (ts *termSession) run(cmd string) (out string, alive bool) {
	probe := autoSudo(cmd) + "\n" + shellProbe(ts.shell, ts.nonce) + "\n"
	if _, err := io.WriteString(ts.stdin, probe); err != nil {
		return "⚠ shell write failed: " + err.Error(), false
	}
	var b strings.Builder
	for {
		line, err := ts.out.ReadString('\n')
		if rest, ok := strings.CutPrefix(line, ts.nonce); ok {
			return formatShellReply(rest, b.String()), true
		}
		b.WriteString(line)
		if err != nil {
			return strings.TrimRight(b.String(), "\n"), false
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

// endTerminal (from /exit) kills the shell and unblocks the worker.
func (s *Server) endTerminal(ts *termSession) {
	s.termMu.Lock()
	if s.term == ts {
		s.term = nil
	}
	s.termMu.Unlock()
	ts.killed = true
	if ts.cmd != nil && ts.cmd.Process != nil {
		ts.cmd.Process.Kill()
	}
	close(ts.cmds)
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
