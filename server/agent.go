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
Reply in the language the owner wrote in (Portuguese gets Portuguese).

## Autonomy

You run with full permissions on the owner's own machine. A turn is killed
after ~15 minutes — for long jobs, do a useful chunk and report progress so
the owner can say "continue". If a task is ambiguous or destructive beyond
what was asked, ask a short question before acting instead of guessing.

## Memory (memoria MCP)

The memoria wiki is your long-term memory across sessions:

- memoria_search before any non-trivial task — past decisions, gotchas and
  rules about this machine and the owner's projects live there.
- memoria_write_page the moment you learn something durable (a decision, a
  gotcha, a credential location, a rule the owner states) — unsaved findings
  die with the session.
- memoria_recall to revisit what an earlier session did.

Sessions are captured automatically by hooks — no manual logging needed.

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
` + "\n" + sendFileContract + "\n" + taskContract

// sendFileContract teaches the agent the file exchange over telegram; also
// appended to pre-existing workspaces by ensureAgentDir.
const sendFileContract = `## Files over Telegram

Files the owner sends arrive in the task text as "[file: /abs/path]" markers
— read them from disk. To deliver a file (photo, PDF, any document) back to
the owner's phone, put this line alone on its own line in your final message:

TOOL:send_file {"path":"/absolute/path/to/file"}

omni uploads the file and replaces the line with a confirmation. Absolute
paths only; images arrive as photos, everything else as documents.
`

// taskContract teaches agent sessions the long-task machinery; also appended
// to pre-existing workspaces by ensureAgentDir.
const taskContract = `## Long tasks (checkpointed)

For a goal too big for one session (hours of work, many sources), hand it to
omni's task runner instead of grinding through it here — it drives autonomous
fresh-context agent steps against a checkpoint file and survives restarts.
Put this line alone on its own line in your final message:

TOOL:task_start {"goal":"..."}

Include EVERY relevant detail in the goal — task agents see nothing else from
this session. Each task keeps its state in tasks/<id>/task.md in this
workspace; you may read those files, but never edit a running task's
checkpoint — the owner steers it with /task #<id> <text>.
`

// ensureAgentDir creates the workspace and seeds the instruction files; an
// existing file is never overwritten — new contract sections are appended.
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
	// migrate older workspaces: append missing contract sections once; owner
	// edits are preserved, nothing is overwritten
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		path := filepath.Join(dir, f)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		orig := len(raw)
		for _, c := range []struct{ marker, section string }{
			{"send_file", sendFileContract},
			{"task_start", taskContract},
		} {
			if strings.Contains(string(raw), c.marker) {
				continue
			}
			raw = append(raw, []byte("\n"+c.section)...)
		}
		if len(raw) == orig {
			continue
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
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
	send := text + personalityMarker() // style rides the wire; history keeps the owner's words
	var reply, vendorID string
	var u callUsage
	switch sess.Provider {
	case "openai":
		reply, vendorID, u, err = runCodexAgent(ctx, sess.VendorSessionID, send)
	default:
		reply, vendorID, u, err = runClaudeAgent(ctx, sess.VendorSessionID, send)
	}
	if err != nil {
		return "⚠ " + err.Error()
	}
	s.recordUsage(sess.Provider, u)
	if u.ctx > 0 {
		s.store.SetSessionCtx(sess.ID, u.ctx) // best-effort, /context only
	}
	// claude forks a new session id every resumed turn — a missed write here
	// orphans the vendor session, so failures surface instead of hiding
	if vendorID != "" && vendorID != sess.VendorSessionID {
		if err := s.store.SetVendorSessionID(sess.ID, vendorID); err != nil {
			return "⚠ " + err.Error()
		}
	}
	// uploads and task starts happen now; history keeps confirmations, never TOOL lines
	reply = s.applyAgentTools(ctx, reply)
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

// claudeResult is claude's -p --output-format json result shape.
type claudeResult struct {
	Result    string  `json:"result"`
	SessionID string  `json:"session_id"`
	IsError   bool    `json:"is_error"`
	TotalCost float64 `json:"total_cost_usd"`
	Usage     struct {
		In         int64 `json:"input_tokens"`
		Out        int64 `json:"output_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (r claudeResult) usage() callUsage {
	in := r.Usage.In + r.Usage.CacheWrite + r.Usage.CacheRead
	return callUsage{in: in, out: r.Usage.Out, ctx: in, cost: r.TotalCost}
}

// parseClaudeJSON decodes a claude json result, mapping is_error to an error.
func parseClaudeJSON(out string) (claudeResult, error) {
	var res claudeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return res, fmt.Errorf("claude: bad json output: %v", err)
	}
	if res.IsError {
		return res, fmt.Errorf("claude: %s", res.Result)
	}
	return res, nil
}

// parseCodexEvents scans a codex --json JSONL stream for the thread id
// ({"type":"thread.started"}) and token usage ({"type":"turn.completed"}).
func parseCodexEvents(out string) (threadID string, u callUsage) {
	for _, line := range strings.Split(out, "\n") {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Usage    struct {
				In  int64 `json:"input_tokens"`
				Out int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			threadID = ev.ThreadID
		case "turn.completed":
			u.in += ev.Usage.In
			u.out += ev.Usage.Out
			u.ctx = ev.Usage.In // last turn's input ≈ current context size
		}
	}
	return threadID, u
}

// runClaudeAgent runs one un-bare claude turn in the workspace; the json
// output carries the reply, usage and the session id to resume next turn.
func runClaudeAgent(ctx context.Context, vendorID, text string) (reply, newVendorID string, u callUsage, err error) {
	args := []string{"-p", text, "--output-format", "json", "--dangerously-skip-permissions"}
	if vendorID != "" {
		args = append(args, "--resume", vendorID)
	}
	out, err := runCLI(ctx, agentDir(), agentTimeout, "claude", args...)
	if err != nil {
		return "", "", callUsage{}, err
	}
	res, err := parseClaudeJSON(out)
	if err != nil {
		return "", "", callUsage{}, err
	}
	return res.Result, res.SessionID, res.usage(), nil
}

// runCodexAgent runs one un-bare codex turn in the workspace: the final
// message lands in a temp file via -o, the session id and usage come from the
// --json event stream.
func runCodexAgent(ctx context.Context, vendorID, text string) (reply, newVendorID string, u callUsage, err error) {
	tmp, err := os.CreateTemp("", "omni-agent-*")
	if err != nil {
		return "", "", callUsage{}, err
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
		return "", "", callUsage{}, err
	}
	newVendorID, u = parseCodexEvents(out)
	raw, err := os.ReadFile(tmp.Name())
	if err != nil {
		return "", "", callUsage{}, err
	}
	return string(raw), newVendorID, u, nil
}
