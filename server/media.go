package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// gatedFile mirrors gatedAnswer for photo/document messages: pairing FIRST
// (an unapproved sender must never trigger a download), then the file lands
// in the workspace inbox and caption + path flow into the active session
// like any text message.
func (s *Server) gatedFile(ctx context.Context, fromID int64, f tgFile) tgReply {
	if ok, r := s.gate(fromID); !ok {
		return r
	}
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return tgReply{Text: "⚠ telegram not connected"}
	}
	path, err := saveInbox(ctx, tg, f)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	text := "[file: " + path + "]"
	if f.Caption != "" {
		text = f.Caption + "\n\n" + text
	}
	return s.handleMessage(ctx, text)
}

// saveInbox downloads one telegram file into filesDir()/inbox with a
// timestamp-prefixed sanitized name; returns the absolute path.
func saveInbox(ctx context.Context, tg *Telegram, f tgFile) (string, error) {
	base := filesDir()
	if base == "" {
		return "", fmt.Errorf("inbox: cannot resolve config dir")
	}
	dir := filepath.Join(base, "inbox")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := sanitizeName(f.Name)
	if name == "" {
		name = "photo.jpg" // telegram photos are always jpeg
	}
	stem := time.Now().Format("20060102-150405") + "-"
	path := filepath.Join(dir, stem+name)
	for n := 2; ; n++ { // albums land several photos in the same second
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s%d-%s", stem, n, name))
	}
	if err := tg.downloadFile(ctx, f.ID, path); err != nil {
		return "", err
	}
	return path, nil
}

// sanitizeName reduces a client-supplied filename to a safe basename;
// nothing usable ("", ".", "..") comes back empty for the caller's fallback.
func sanitizeName(name string) string {
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '-'
	}, name)
}

// filePrompt is the chat-mode file toolkit contract, injected into every chat
// prompt next to the cron section. Chat llms stay bare — these tools run
// server-side, so the model gets file powers without host access leaking in.
func filePrompt() string {
	return `## Files

You can manage files in the owner's omni folder (` + filesDir() + `) with
these tool lines (each alone on its own line, nothing else on it; lines run
in order, so you can write a file and send it in the same reply):
TOOL:write_file {"path":"notes.txt","content":"..."}
TOOL:read_file {"path":"notes.txt"}
TOOL:edit_file {"path":"x.txt","find":"old text","replace":"new text"}
TOOL:delete_file {"path":"x.txt"}
TOOL:send_file {"path":"report.pdf"}
TOOL:analyze_file {"path":"inbox/photo.jpg","question":"what is this?"}

write_file creates or overwrites; edit_file replaces every exact match;
send_file delivers the file to the owner's phone (images as photos, else
documents); read_file works on TEXT files only and feeds the content back to
you in the same turn — emit the line alone and you will be asked to continue
with the content in view. You cannot see images, PDFs or other
binary content yourself — use analyze_file for those: it has the agent look
at the file and returns its answer in place of the tool line, so use it
whenever the owner asks about a photo or document they sent (it is slow —
mention you are taking a look). Give paths relative to that folder
(subfolders are fine); anything outside it is refused. Files the owner sends
arrive under inbox/ and show as "[file: /abs/path]" markers in their
message. You have NO other access to the owner's computer — no shell, no
browser, no listing directories. If a task needs those, tell the owner to
start an agent session with /agent.
Never claim a file action happened without emitting its tool line.`
}

// readFileCap bounds what read_file inlines into the chat.
const readFileCap = 8 << 10

// filesDir is the one folder chat file tools manage and inbound files land
// in: ~/.config/omni/files (dev: omni-dev), next to config.yaml so the owner
// has a single omni folder to browse and back up.
func filesDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, app, "files")
}

// resolveJailed confines a chat tool path to filesDir: relative paths join
// it, absolute ones must already live inside, ".." escapes are refused.
func resolveJailed(path string) (string, error) {
	base := filesDir()
	if base == "" {
		return "", fmt.Errorf("cannot resolve config dir")
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if p != base && !strings.HasPrefix(p, base+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside %s", base)
	}
	return p, nil
}

// resolveWorkspace anchors agent-mode send paths: absolute anywhere (the
// agent has full PC control already), relative in its cwd, the workspace.
func resolveWorkspace(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	base := agentDir()
	if base == "" {
		return "", fmt.Errorf("cannot resolve home")
	}
	return filepath.Join(base, path), nil
}

// fileTool executes one chat-mode file tool, returning the confirmation that
// replaces the TOOL line. Everything is jailed to filesDir — chat file work
// stays inside the one owner-visible omni folder.
func (s *Server) fileTool(ctx context.Context, name, args string) string {
	if name == "send_file" {
		return s.sendFile(ctx, args, resolveJailed)
	}
	var a struct{ Path, Content, Find, Replace, Question string }
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "⚠ bad tool arguments: " + err.Error()
	}
	if a.Path == "" {
		return "⚠ " + name + ": path required"
	}
	path, err := resolveJailed(a.Path)
	if err != nil {
		return "⚠ " + name + ": " + err.Error()
	}
	switch name {
	case "write_file":
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "⚠ write_file: " + err.Error()
		}
		if err := os.WriteFile(path, []byte(a.Content), 0o644); err != nil {
			return "⚠ write_file: " + err.Error()
		}
		return fmt.Sprintf("📝 wrote %s (%d bytes)", path, len(a.Content))
	case "read_file":
		raw, err := os.ReadFile(path)
		if err != nil {
			return "⚠ read_file: " + err.Error()
		}
		if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) {
			return fmt.Sprintf("⚠ read_file: %s is binary (%d bytes) — read_file is for text; use analyze_file to look inside it or send_file to deliver it", path, len(raw))
		}
		if len(raw) > readFileCap {
			cut := readFileCap
			for cut > 0 && !utf8.RuneStart(raw[cut]) {
				cut--
			}
			return fmt.Sprintf("📄 %s (first %d of %d bytes):\n%s", path, cut, len(raw), raw[:cut])
		}
		return fmt.Sprintf("📄 %s:\n%s", path, raw)
	case "edit_file":
		if a.Find == "" {
			return "⚠ edit_file: find required"
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "⚠ edit_file: " + err.Error()
		}
		n := strings.Count(string(raw), a.Find)
		if n == 0 {
			return "⚠ edit_file: text not found in " + path
		}
		if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(raw), a.Find, a.Replace)), 0o644); err != nil {
			return "⚠ edit_file: " + err.Error()
		}
		return fmt.Sprintf("✏ %s: %d replacement(s)", path, n)
	case "delete_file":
		fi, err := os.Stat(path)
		if err != nil {
			return "⚠ delete_file: " + err.Error()
		}
		if !fi.Mode().IsRegular() {
			return "⚠ delete_file: not a regular file: " + path
		}
		if err := os.Remove(path); err != nil {
			return "⚠ delete_file: " + err.Error()
		}
		return "🗑 deleted " + path
	case "analyze_file":
		if _, err := os.Stat(path); err != nil {
			return "⚠ analyze_file: " + err.Error()
		}
		return s.analyzeFile(ctx, path, a.Question)
	}
	return "⚠ unknown tool " + name
}

// analyzeFile answers a question about one file the chat llm cannot read
// itself (image, pdf, any binary) with a one-shot fresh-context agent run —
// the same machinery as cron agent jobs, so chat stays bare while the answer
// still lands in the same reply.
func (s *Server) analyzeFile(ctx context.Context, path, question string) string {
	if question == "" {
		question = "Describe it."
	}
	if err := ensureAgentDir(); err != nil {
		return "⚠ analyze_file: " + err.Error()
	}
	task := "Read the file at " + path + " and answer the owner's question about it. " +
		"Plain text, concise, in the owner's language. Question: " + question
	provider, _ := agentProvider()
	var reply string
	var u callUsage
	var err error
	if provider == "openai" {
		reply, _, u, err = runCodexAgent(ctx, "", task)
	} else {
		reply, _, u, err = runClaudeAgent(ctx, "", task)
	}
	if err != nil {
		return "⚠ analyze_file: " + err.Error()
	}
	s.recordUsage(provider, u)
	if strings.TrimSpace(reply) == "" {
		return "⚠ analyze_file: empty reply"
	}
	return strings.TrimSpace(reply)
}

// applySendFile executes TOOL:send_file lines in an agent reply: uploads to
// every approved chat now, replaces each line with a confirmation. Agent
// replies get only this tool — the vendor CLI has its own real tools, and
// cron tools never run in agent mode.
func (s *Server) applySendFile(ctx context.Context, reply string) string {
	if !strings.Contains(reply, "TOOL:send_file") {
		return reply
	}
	lines := strings.Split(reply, "\n")
	for i, line := range lines {
		name, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name != "TOOL:send_file" {
			continue
		}
		lines[i] = s.sendFile(ctx, args, resolveWorkspace)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// sendFile uploads one local file to every approved chat: images as photos,
// everything else as documents; a failed photo retries as a document
// (>10MB or odd dimensions). Returns the confirmation replacing the TOOL
// line. resolve sets the caller's path rules: jailed for chat, workspace for
// agents.
func (s *Server) sendFile(ctx context.Context, args string, resolve func(string) (string, error)) string {
	var a struct{ Path string }
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "⚠ bad tool arguments: " + err.Error()
	}
	path, err := resolve(a.Path)
	if err != nil {
		return "⚠ send_file: " + err.Error()
	}
	a.Path = path
	fi, err := os.Stat(a.Path)
	if err != nil || !fi.Mode().IsRegular() {
		return "⚠ send_file: not a file: " + a.Path
	}
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return "⚠ send_file: telegram not connected"
	}
	method, field := "sendDocument", "document"
	switch strings.ToLower(filepath.Ext(a.Path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif":
		method, field = "sendPhoto", "photo"
	}
	for _, id := range s.ownerChats() {
		err := tg.upload(ctx, method, field, id, a.Path)
		if err != nil && method == "sendPhoto" {
			log.Printf("send_file: photo chat %d: %v — retrying as document", id, err)
			err = tg.upload(ctx, "sendDocument", "document", id, a.Path)
		}
		if err != nil {
			return "⚠ send_file: " + err.Error()
		}
	}
	return "📎 sent " + filepath.Base(a.Path)
}
