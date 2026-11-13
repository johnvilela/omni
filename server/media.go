package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	root, base, err := openFilesRoot()
	if err != nil {
		return "", fmt.Errorf("inbox: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll("inbox", 0o700); err != nil {
		return "", err
	}
	name := sanitizeName(f.Name)
	if name == "" {
		name = "photo.jpg" // telegram photos are always jpeg
	}
	stem := time.Now().Format("20060102-150405") + "-"
	rel := filepath.Join("inbox", stem+name)
	for n := 2; ; n++ { // albums land several photos in the same second
		if _, err := root.Lstat(rel); os.IsNotExist(err) {
			break
		}
		rel = filepath.Join("inbox", fmt.Sprintf("%s%d-%s", stem, n, name))
	}
	out, err := root.Create(rel)
	if err != nil {
		return "", err
	}
	if err := tg.downloadFile(ctx, f.ID, out); err != nil {
		out.Close()
		root.Remove(rel) // no partial files in the inbox
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return filepath.Join(base, rel), nil
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

// openFilesRoot opens filesDir as an os.Root: every file operation through
// it is confined by the kernel (openat + no symlink escape), not by string
// comparison, so a symlink planted inside the folder cannot reach outside.
// The caller closes it. base is the absolute dir for display.
func openFilesRoot() (*os.Root, string, error) {
	base := filesDir()
	if base == "" {
		return nil, "", fmt.Errorf("cannot resolve config dir")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, "", err
	}
	return root, base, nil
}

// jailRel turns a chat tool path into a path relative to filesDir: relative
// paths are taken as-is, absolute ones must already live inside, ".." escapes
// are refused lexically here and by os.Root again at open time.
func jailRel(base, path string) (string, error) {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if p != base && !strings.HasPrefix(p, base+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside %s", base)
	}
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return "", fmt.Errorf("path outside %s", base)
	}
	return rel, nil
}

// openJailed is the chat send_file opener: through the files root.
func openJailed(path string) (*os.File, error) {
	root, base, err := openFilesRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := jailRel(base, path)
	if err != nil {
		return nil, err
	}
	return root.Open(rel)
}

// openWorkspace is the agent-mode send_file opener: absolute anywhere (the
// agent already has that reach), relative in its cwd, the workspace.
func openWorkspace(path string) (*os.File, error) {
	if filepath.IsAbs(path) {
		return os.Open(path)
	}
	base := agentDir()
	if base == "" {
		return nil, fmt.Errorf("cannot resolve home")
	}
	return os.Open(filepath.Join(base, path))
}

// fileTool executes one chat-mode file tool, returning the confirmation that
// replaces the TOOL line. Everything is jailed to filesDir — chat file work
// stays inside the one owner-visible omni folder.
func (s *Server) fileTool(ctx context.Context, name, args string) string {
	if name == "send_file" {
		return s.sendFile(ctx, args, openJailed)
	}
	var a struct{ Path, Content, Find, Replace, Question string }
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "⚠ bad tool arguments: " + err.Error()
	}
	if a.Path == "" {
		return "⚠ " + name + ": path required"
	}
	root, base, err := openFilesRoot()
	if err != nil {
		return "⚠ " + name + ": " + err.Error()
	}
	defer root.Close()
	rel, err := jailRel(base, a.Path)
	if err != nil {
		return "⚠ " + name + ": " + err.Error()
	}
	path := filepath.Join(base, rel) // display only; every op below goes through root
	switch name {
	case "write_file":
		if dir := filepath.Dir(rel); dir != "." {
			if err := root.MkdirAll(dir, 0o700); err != nil {
				return "⚠ write_file: " + err.Error()
			}
		}
		if err := root.WriteFile(rel, []byte(a.Content), 0o644); err != nil {
			return "⚠ write_file: " + err.Error()
		}
		return fmt.Sprintf("📝 wrote %s (%d bytes)", path, len(a.Content))
	case "read_file":
		raw, err := root.ReadFile(rel)
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
		raw, err := root.ReadFile(rel)
		if err != nil {
			return "⚠ edit_file: " + err.Error()
		}
		n := strings.Count(string(raw), a.Find)
		if n == 0 {
			return "⚠ edit_file: text not found in " + path
		}
		if err := root.WriteFile(rel, []byte(strings.ReplaceAll(string(raw), a.Find, a.Replace)), 0o644); err != nil {
			return "⚠ edit_file: " + err.Error()
		}
		return fmt.Sprintf("✏ %s: %d replacement(s)", path, n)
	case "delete_file":
		fi, err := root.Lstat(rel) // Lstat: a symlink is "not a regular file", not followed
		if err != nil {
			return "⚠ delete_file: " + err.Error()
		}
		if !fi.Mode().IsRegular() {
			return "⚠ delete_file: not a regular file: " + path
		}
		if err := root.Remove(rel); err != nil {
			return "⚠ delete_file: " + err.Error()
		}
		return "🗑 deleted " + path
	case "analyze_file":
		// the path goes to an external agent as text, so os.Root cannot
		// guard the read: resolve symlinks and require the real file to sit
		// under the real files dir
		real, err := realInside(base, path)
		if err != nil {
			return "⚠ analyze_file: " + err.Error()
		}
		return s.analyzeFile(ctx, real, a.Question)
	}
	return "⚠ unknown tool " + name
}

// realInside resolves every symlink in path and checks the result still
// lives under base (also symlink-resolved). Used only where a path string
// leaves the process; everything else goes through os.Root.
func realInside(base, path string) (string, error) {
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if real != realBase && !strings.HasPrefix(real, realBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside %s", base)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	return real, nil
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
	provider, _, err := s.agentProvider()
	if err != nil {
		return "⚠ analyze_file: " + err.Error()
	}
	var reply string
	var u callUsage
	reply, _, u, err = s.runAgentModel(ctx, "", "file.analyze", provider, "", task)
	if err != nil {
		return "⚠ analyze_file: " + err.Error()
	}
	s.recordUsage(provider, u)
	if strings.TrimSpace(reply) == "" {
		return "⚠ analyze_file: empty reply"
	}
	return strings.TrimSpace(reply)
}

// agentTools are the TOOL lines honored in agent-session and cron replies:
// task_start plus the cron tools, so a prompt-command session can manage its
// reminders. The vendor CLI has its own real tools; chat file tools never run
// in agent mode.
var agentTools = map[string]bool{"task_start": true, "cron_add": true, "cron_edit": true, "cron_delete": true}

// applyAgentTools executes the agentTools TOOL lines in an agent reply.
// Task-step replies deliberately get only applySendFile (their DONE summary)
// — a step agent must never start sub-tasks.
// ponytail: confirmations replace TOOL lines in omni's history only — the
// vendor session never learns e.g. the cron ID it just created; a fresh
// run's cronPrompt listing is the recovery path.
func (s *Server) applyAgentTools(ctx context.Context, reply string) string {
	reply = s.applySendFile(ctx, reply)
	if !strings.Contains(reply, "TOOL:") {
		return reply
	}
	lines := strings.Split(reply, "\n")
	for i, line := range lines {
		name, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		tool, isTool := strings.CutPrefix(name, "TOOL:")
		if !ok || !isTool || !agentTools[tool] {
			continue
		}
		lines[i] = s.runTool(ctx, "", tool, args)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// applySendFile executes TOOL:send_file lines in an agent reply: uploads to
// every approved chat now, replaces each line with a confirmation.
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
		lines[i] = s.sendFile(ctx, args, openWorkspace)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// sendFile uploads one local file to every approved chat: images as photos,
// everything else as documents; a failed photo retries as a document
// (>10MB or odd dimensions). Returns the confirmation replacing the TOOL
// line. open sets the caller's path rules: jailed for chat, workspace for
// agents; the opened file (not its path) is what gets uploaded, so a check
// here cannot be raced by swapping the path underneath.
func (s *Server) sendFile(ctx context.Context, args string, open func(string) (*os.File, error)) string {
	var a struct{ Path string }
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "⚠ bad tool arguments: " + err.Error()
	}
	f, err := open(a.Path)
	if err != nil {
		if strings.Contains(err.Error(), "path outside") {
			return "⚠ send_file: " + err.Error()
		}
		return "⚠ send_file: not a file: " + a.Path
	}
	defer f.Close()
	fi, err := f.Stat()
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
	name := filepath.Base(a.Path)
	for _, id := range s.ownerChats() {
		f.Seek(0, io.SeekStart)
		err := tg.upload(ctx, method, field, id, name, f)
		if err != nil && method == "sendPhoto" {
			log.Printf("send_file: photo chat %d: %v — retrying as document", id, err)
			f.Seek(0, io.SeekStart)
			err = tg.upload(ctx, "sendDocument", "document", id, name, f)
		}
		if err != nil {
			return "⚠ send_file: " + err.Error()
		}
	}
	return "📎 sent " + filepath.Base(a.Path)
}
