package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeMediaAPI records every bot-API call as {"method": name, ...} on calls —
// multipart uploads land as {"method", "chat_id", "field", "filename",
// "bytes"} — and replies ok. Downloads work: getFile resolves to a fixed
// path served with "JPEG" bytes.
func fakeMediaAPI(t *testing.T, calls chan<- map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file/botTOKEN/photos/p.jpg", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "JPEG")
	})
	mux.HandleFunc("/botTOKEN/", func(w http.ResponseWriter, r *http.Request) {
		rec := map[string]any{"method": strings.TrimPrefix(r.URL.Path, "/botTOKEN/")}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			r.ParseMultipartForm(1 << 20)
			rec["chat_id"] = r.FormValue("chat_id")
			for field, fs := range r.MultipartForm.File {
				f, _ := fs[0].Open()
				raw, _ := io.ReadAll(f)
				f.Close()
				rec["field"], rec["filename"], rec["bytes"] = field, fs[0].Filename, string(raw)
			}
		} else {
			json.NewDecoder(r.Body).Decode(&rec)
		}
		calls <- rec
		fmt.Fprint(w, `{"ok":true,"result":{"file_path":"photos/p.jpg","message_id":100}}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newMediaServer is a chat test server with one approved pairing (chat 42)
// and a recording telegram attached directly (no poller).
func newMediaServer(t *testing.T) (*Server, *promptRecorder, chan map[string]any) {
	t.Helper()
	srv, store, rec := newChatTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := store.AddPairing("telegram", "42", "CODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePairing("telegram", "CODE"); err != nil {
		t.Fatal(err)
	}
	calls := make(chan map[string]any, 32)
	fake := fakeMediaAPI(t, calls)
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	return srv, rec, calls
}

// TestGatedFileUnpairedNoDownload locks the security constraint: an
// unapproved sender gets the pairing flow and never triggers a download.
func TestGatedFileUnpairedNoDownload(t *testing.T) {
	srv, _ := newTestServer(t)
	var hits atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	t.Cleanup(fake.Close)
	srv.tg = NewTelegram(fake.URL, "TOKEN")

	r := srv.gatedFile(context.Background(), 99, tgFile{ID: "f1", Caption: "hi"})
	if !strings.Contains(r.Text, "access not configured") {
		t.Fatalf("reply = %q; want pairing instructions", r.Text)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("telegram API hit %d times; want 0 for an unpaired sender", n)
	}
}

// TestGatedFileInbox: an approved photo lands in the workspace inbox and
// caption + [file: path] flow into the active session's prompt.
func TestGatedFileInbox(t *testing.T) {
	srv, rec, _ := newMediaServer(t)
	seedSession(t, srv)

	r := srv.gatedFile(context.Background(), 42, tgFile{ID: "p1", Caption: "describe this"})
	if r.Text != "" {
		t.Fatalf("reply = %q; want queued silence", r.Text)
	}
	matches, err := filepath.Glob(filepath.Join(filesDir(), "inbox", "*-photo.jpg"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("inbox glob = %v, %v; want one *-photo.jpg", matches, err)
	}
	raw, _ := os.ReadFile(matches[0])
	if string(raw) != "JPEG" {
		t.Fatalf("inbox content = %q; want JPEG", raw)
	}
	want := "describe this\n\n[file: " + matches[0] + "]"
	waitFor(t, func() bool {
		for _, p := range rec.all() {
			if strings.Contains(p, want) {
				return true
			}
		}
		return false
	})
}

func TestSendFileTool(t *testing.T) {
	srv, _, calls := newMediaServer(t) // recorder unused: no chat turn runs
	dir := t.TempDir()
	jpg := filepath.Join(dir, "shot.jpg")
	pdf := filepath.Join(dir, "report.pdf")
	for _, p := range []string{jpg, pdf} {
		if err := os.WriteFile(p, []byte("DATA"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	reply := srv.applySendFile(context.Background(),
		"here you go\nTOOL:send_file {\"path\":\""+jpg+"\"}\nbye")
	if reply != "here you go\n📎 sent shot.jpg\nbye" {
		t.Fatalf("reply = %q; want the TOOL line replaced with 📎", reply)
	}
	up := nextCall(t, calls, "sendPhoto")
	if up["chat_id"] != "42" || up["field"] != "photo" || up["filename"] != "shot.jpg" || up["bytes"] != "DATA" {
		t.Fatalf("sendPhoto call = %v", up)
	}

	if got := srv.applySendFile(context.Background(), "TOOL:send_file {\"path\":\""+pdf+"\"}"); got != "📎 sent report.pdf" {
		t.Fatalf("reply = %q; want 📎 sent report.pdf", got)
	}
	if up := nextCall(t, calls, "sendDocument"); up["field"] != "document" {
		t.Fatalf("sendDocument call = %v", up)
	}

	missing := filepath.Join(dir, "nope.pdf")
	if got := srv.applySendFile(context.Background(), "TOOL:send_file {\"path\":\""+missing+"\"}"); !strings.Contains(got, "⚠ send_file: not a file") {
		t.Fatalf("reply = %q; want ⚠ not a file", got)
	}
	noCall(t, calls)

	prose := "plain answer, no tools"
	if got := srv.applySendFile(context.Background(), prose); got != prose {
		t.Fatalf("prose = %q; want passthrough", got)
	}
}

// TestChatFileTools: the chat-mode file toolkit — create, read, edit, delete
// and send run server-side off TOOL lines; relative paths land in the
// workspace; a write and a send combine in one reply, in order.
func TestChatFileTools(t *testing.T) {
	srv, _, calls := newMediaServer(t)
	ctx := context.Background()

	got := srv.applyTools(ctx, `TOOL:write_file {"path":"notes.txt","content":"hello world"}`)
	path := filepath.Join(filesDir(), "notes.txt")
	if got != fmt.Sprintf("📝 wrote %s (11 bytes)", path) {
		t.Fatalf("write reply = %q", got)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "hello world" {
		t.Fatalf("workspace file = %q, %v; want hello world", raw, err)
	}

	got = srv.applyTools(ctx, `TOOL:read_file {"path":"notes.txt"}`)
	if !strings.Contains(got, "📄 "+path) || !strings.Contains(got, "hello world") {
		t.Fatalf("read reply = %q", got)
	}

	// binary files never dump raw bytes into the chat (the JPEG incident)
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, []byte("JFIF junk")...)
	if err := os.WriteFile(filepath.Join(filesDir(), "shot.jpg"), jpeg, 0o644); err != nil {
		t.Fatal(err)
	}
	got = srv.applyTools(ctx, `TOOL:read_file {"path":"shot.jpg"}`)
	if !strings.Contains(got, "⚠ read_file:") || !strings.Contains(got, "binary") || !strings.Contains(got, "analyze_file") {
		t.Fatalf("binary read reply = %q; want ⚠ binary refusal pointing to analyze_file", got)
	}

	got = srv.applyTools(ctx, `TOOL:edit_file {"path":"notes.txt","find":"world","replace":"omni"}`)
	if !strings.Contains(got, "✏") || !strings.Contains(got, "1 replacement") {
		t.Fatalf("edit reply = %q", got)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "hello omni" {
		t.Fatalf("edited file = %q; want hello omni", raw)
	}
	if got = srv.applyTools(ctx, `TOOL:edit_file {"path":"notes.txt","find":"nope","replace":"x"}`); !strings.Contains(got, "⚠ edit_file: text not found") {
		t.Fatalf("edit miss reply = %q", got)
	}

	// write + send in one reply, executed in order
	got = srv.applyTools(ctx, "here you go\n"+
		`TOOL:write_file {"path":"out.txt","content":"payload"}`+"\n"+
		`TOOL:send_file {"path":"out.txt"}`)
	if !strings.Contains(got, "📝 wrote") || !strings.Contains(got, "📎 sent out.txt") {
		t.Fatalf("combined reply = %q", got)
	}
	up := nextCall(t, calls, "sendDocument")
	if up["filename"] != "out.txt" || up["bytes"] != "payload" {
		t.Fatalf("sendDocument call = %v", up)
	}

	got = srv.applyTools(ctx, `TOOL:delete_file {"path":"notes.txt"}`)
	if got != "🗑 deleted "+path {
		t.Fatalf("delete reply = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after delete: %v", err)
	}
	if got = srv.applyTools(ctx, `TOOL:delete_file {"path":"notes.txt"}`); !strings.Contains(got, "⚠ delete_file:") {
		t.Fatalf("delete gone reply = %q", got)
	}

	// the jail: absolute paths outside filesDir and ".." escapes are refused
	for _, line := range []string{
		`TOOL:read_file {"path":"/etc/passwd"}`,
		`TOOL:write_file {"path":"../escape.txt","content":"x"}`,
		`TOOL:delete_file {"path":"` + filepath.Join(agentDir(), "CLAUDE.md") + `"}`,
		`TOOL:send_file {"path":"/etc/hostname"}`,
	} {
		if got := srv.applyTools(ctx, line); !strings.Contains(got, "path outside") {
			t.Fatalf("jail escape %q = %q; want refusal", line, got)
		}
	}
}

// TestChatPromptCarriesFileTools: every chat prompt teaches the file toolkit.
func TestChatPromptCarriesFileTools(t *testing.T) {
	srv, _, rec := newChatTestServer(t)
	if _, err := srv.ChatAnswer(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	prompts := rec.all()
	if len(prompts) == 0 || !strings.Contains(prompts[0], "TOOL:write_file") ||
		!strings.Contains(prompts[0], "TOOL:send_file") ||
		!strings.Contains(prompts[0], "TOOL:analyze_file") ||
		!strings.Contains(prompts[0], "/agent") {
		t.Fatalf("chat prompt missing the file toolkit: %.300q", prompts)
	}
}

// TestAnalyzeFileTool: analyze_file hands a jailed file to a one-shot agent
// run and the agent's answer replaces the TOOL line.
func TestAnalyzeFileTool(t *testing.T) {
	srv, _, _ := newMediaServer(t)
	argsFile, _ := writeAgentFakes(t) // default llm unset → claude fake, replies pong
	jpg := filepath.Join(filesDir(), "inbox", "x.jpg")
	if err := os.MkdirAll(filepath.Dir(jpg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jpg, []byte{0xFF, 0xD8, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}

	got := srv.applyTools(context.Background(), `TOOL:analyze_file {"path":"inbox/x.jpg","question":"what is this?"}`)
	if got != "pong" {
		t.Fatalf("analyze reply = %q; want the agent answer", got)
	}
	args := strings.Join(readLines(t, argsFile), " ")
	if !strings.Contains(args, jpg) || !strings.Contains(args, "what is this?") {
		t.Fatalf("agent task missing path/question: %q", args)
	}

	// missing file: refused before any agent run
	if got := srv.applyTools(context.Background(), `TOOL:analyze_file {"path":"nope.jpg","question":"?"}`); !strings.Contains(got, "⚠ analyze_file:") {
		t.Fatalf("missing-file reply = %q", got)
	}
	// jail still applies
	if got := srv.applyTools(context.Background(), `TOOL:analyze_file {"path":"/etc/passwd","question":"?"}`); !strings.Contains(got, "path outside") {
		t.Fatalf("jail reply = %q", got)
	}
}

// TestSendFilePhotoFallback: a rejected sendPhoto (>10MB, odd dimensions)
// retries the same chat as a document.
func TestSendFilePhotoFallback(t *testing.T) {
	srv, store, _ := newChatTestServer(t)
	store.AddPairing("telegram", "42", "CODE")
	store.ApprovePairing("telegram", "CODE")
	calls := make(chan map[string]any, 8)
	mux := http.NewServeMux()
	mux.HandleFunc("/botTOKEN/", func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/botTOKEN/")
		calls <- map[string]any{"method": method}
		if method == "sendPhoto" {
			fmt.Fprint(w, `{"ok":false,"description":"Bad Request: PHOTO_INVALID_DIMENSIONS"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
	})
	fake := httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	srv.tg = NewTelegram(fake.URL, "TOKEN")

	jpg := filepath.Join(t.TempDir(), "big.jpg")
	if err := os.WriteFile(jpg, []byte("DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := srv.sendFile(context.Background(), `{"path":"`+jpg+`"}`, resolveWorkspace); got != "📎 sent big.jpg" {
		t.Fatalf("sendFile = %q; want 📎 sent big.jpg", got)
	}
	nextCall(t, calls, "sendPhoto")
	nextCall(t, calls, "sendDocument")
}

func TestSanitizeInboxName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":        "report.pdf",
		"../../etc/passwd":  "passwd",
		"we ird$name.pdf":   "we-ird-name.pdf",
		"olá çedilha.png":   "olá-çedilha.png",
		"CON_fig-v2.tar.gz": "CON_fig-v2.tar.gz",
		"":                  "", // photos carry no name — caller falls back
		"..":                "",
	} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestSaveInboxCollision: two same-named files never overwrite each other
// (albums land several photos in the same second).
func TestSaveInboxCollision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	calls := make(chan map[string]any, 8)
	fake := fakeMediaAPI(t, calls)
	tg := NewTelegram(fake.URL, "TOKEN")

	p1, err := saveInbox(context.Background(), tg, tgFile{ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := saveInbox(context.Background(), tg, tgFile{ID: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("both photos saved to %s; want distinct paths", p1)
	}
	for _, p := range []string{p1, p2} {
		if raw, err := os.ReadFile(p); err != nil || string(raw) != "JPEG" {
			t.Fatalf("inbox %s = %q, %v; want JPEG", p, raw, err)
		}
	}
}
