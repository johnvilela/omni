package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// agentTimeout caps one agent turn; the seed instructions tell the agent to
// report partial progress on longer jobs.
const agentTimeout = 15 * time.Minute

// agentDir is the agent session workspace: the vendor CLI's cwd, the seeded
// CLAUDE.md/AGENTS.md home and the chrome profile. Dev/prod isolated via app,
// like dbPath.
func agentDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, app, "agent")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", app, "agent")
}

// agentSeed becomes CLAUDE.md and AGENTS.md in a fresh workspace — written
// once, never overwritten, so the owner can edit it freely.
const agentSeed = `# omni agent

You are omni's agent, operating the owner's PC. Tasks arrive from the owner's
phone via Telegram; your final message is read there. This directory is your
workspace.

## Output

Keep the final message short and plain-text: no markdown tables, no headers,
no code fences unless the owner asked for code. Lead with the outcome.

## Autonomy

You run with full permissions on the owner's own machine. A turn is killed
after ~15 minutes — for long jobs, do a useful chunk and report progress so
the owner can say "continue".

## Browser

Use playwright-cli against the shared chromium profile (logins live there —
NEVER delete chrome-profile/):

1. If curl -s localhost:9222/json/version fails, launch the browser:
   chromium --remote-debugging-port=9222 --user-data-dir="$PWD/chrome-profile" &
   (add --headless=new when no display is available)
2. playwright-cli attach --cdp=http://localhost:9222
3. Drive it: goto <url>, snapshot, find <text>, click <ref>, fill <ref> <text>, eval <js>
4. When done: playwright-cli detach (leave chromium running for next time)

## LinkedIn (and any outward posting)

Never post, comment, message, or send connection requests directly. Draft the
content, reply with the draft, and act only after the owner explicitly
approves in a later message. Reading, searching and browsing are fine.
`

// ensureAgentDir creates the workspace and seeds the instruction files; an
// existing file is never touched.
func ensureAgentDir() error {
	dir := agentDir()
	if dir == "" {
		return fmt.Errorf("agent workspace: cannot resolve home")
	}
	if err := os.MkdirAll(filepath.Join(dir, "chrome-profile"), 0o700); err != nil {
		return err
	}
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(agentSeed), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// agentAnswer continues an agent session: the vendor CLI runs un-bare (user
// settings, tools, memoria hooks) in the workspace and carries its own
// conversation state, so the raw text goes straight through — no composed
// history, no memory injection, no compaction.
func (s *Server) agentAnswer(ctx context.Context, sess Session, text string) string {
	if err := ensureAgentDir(); err != nil { // resumed sessions may outlive the dir
		return "⚠ " + err.Error()
	}
	history, err := s.store.Messages(sess.ID)
	if err != nil {
		return "⚠ " + err.Error()
	}
	// save before asking, same contract as ChatAnswer
	if _, err := s.store.AddMessage(sess.ID, "user", text, time.Now().Unix()); err != nil {
		return "⚠ " + err.Error()
	}
	var reply, vendorID string
	switch sess.Provider {
	case "openai":
		reply, vendorID, err = runCodexAgent(ctx, sess.VendorSessionID, text)
	default:
		reply, vendorID, err = runClaudeAgent(ctx, sess.VendorSessionID, text)
	}
	if err != nil {
		return "⚠ " + err.Error()
	}
	// claude forks a new session id every resumed turn — a missed write here
	// orphans the vendor session, so failures surface instead of hiding
	if vendorID != "" && vendorID != sess.VendorSessionID {
		if err := s.store.SetVendorSessionID(sess.ID, vendorID); err != nil {
			return "⚠ " + err.Error()
		}
	}
	if _, err := s.store.AddMessage(sess.ID, "assistant", reply, time.Now().Unix()); err != nil {
		return "⚠ " + err.Error()
	}
	if len(history) == 0 {
		go s.nameSession(sess.ID, text)
	}
	if strings.TrimSpace(reply) == "" {
		return "(empty reply)" // telegram rejects empty text
	}
	return strings.TrimSpace(reply)
}

// runClaudeAgent runs one un-bare claude turn in the workspace; the json
// output carries both the reply and the session id to resume next turn.
func runClaudeAgent(ctx context.Context, vendorID, text string) (reply, newVendorID string, err error) {
	args := []string{"-p", text, "--output-format", "json", "--dangerously-skip-permissions"}
	if vendorID != "" {
		args = append(args, "--resume", vendorID)
	}
	out, err := runCLI(ctx, agentDir(), agentTimeout, "claude", args...)
	if err != nil {
		return "", "", err
	}
	var res struct {
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
		IsError   bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return "", "", fmt.Errorf("claude: bad json output: %v", err)
	}
	if res.IsError {
		return "", "", fmt.Errorf("claude: %s", res.Result)
	}
	return res.Result, res.SessionID, nil
}

// runCodexAgent runs one un-bare codex turn in the workspace: the final
// message lands in a temp file via -o, the session id comes from the --json
// event stream ({"type":"thread.started","thread_id":"…"}).
func runCodexAgent(ctx context.Context, vendorID, text string) (reply, newVendorID string, err error) {
	tmp, err := os.CreateTemp("", "omni-agent-*")
	if err != nil {
		return "", "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	args := []string{"exec"}
	if vendorID != "" {
		args = append(args, "resume", vendorID)
	}
	args = append(args, "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox",
		"--json", "-o", tmp.Name(), text)
	out, err := runCLI(ctx, agentDir(), agentTimeout, "codex", args...)
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(out, "\n") {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "thread.started" {
			newVendorID = ev.ThreadID
			break
		}
	}
	raw, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", "", err
	}
	return string(raw), newVendorID, nil
}
