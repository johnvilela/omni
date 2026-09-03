package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeAgentFakes puts fake claude/codex scripts on PATH that record args and
// cwd, emit the vendor session id ("vend-r" once --resume/resume appears),
// and reply pong the way each real CLI does (claude: result JSON on stdout;
// codex: JSONL events on stdout, final text into the -o file).
func writeAgentFakes(t *testing.T) (argsFile, pwdFile string) {
	t.Helper()
	bin := t.TempDir()
	argsFile = filepath.Join(bin, "args.txt")
	pwdFile = filepath.Join(bin, "pwd.txt")
	claude := `#!/bin/sh
printf '%s\n' "$@" > ` + argsFile + `
pwd > ` + pwdFile + `
case "$*" in
*--resume*) echo '{"result":"pong","session_id":"vend-r"}' ;;
*) echo '{"result":"pong","session_id":"vend-1"}' ;;
esac
`
	codex := `#!/bin/sh
printf '%s\n' "$@" > ` + argsFile + `
pwd > ` + pwdFile + `
prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
echo pong > "$out"
case "$1 $2" in
"exec resume") echo '{"type":"thread.started","thread_id":"vend-r"}' ;;
*) echo '{"type":"thread.started","thread_id":"vend-1"}' ;;
esac
echo '{"type":"turn.completed"}'
`
	for name, script := range map[string]string{"claude": claude, "codex": codex} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return argsFile, pwdFile
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
}

// TestAgentAnswerClaude locks the un-bare agent contract: full permissions,
// user settings loaded (no bare-mode flags), raw text through, session id
// captured and refreshed on every turn, exchange logged to sqlite.
func TestAgentAnswerClaude(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	argsFile, pwdFile := writeAgentFakes(t)
	store.AddSession("s1", true, "claude")
	store.SetActiveSession("s1")

	sess, _, _ := store.Session("s1")
	reply := srv.agentAnswer(context.Background(), sess, "do the thing")
	if reply != "pong" {
		t.Fatalf("agent reply = %q; want pong", reply)
	}
	want := []string{"-p", "do the thing", "--output-format", "json", "--dangerously-skip-permissions"}
	if got := readLines(t, argsFile); !slices.Equal(got, want) {
		t.Fatalf("claude args = %q; want %q", got, want)
	}
	if got := readLines(t, pwdFile); got[0] != agentDir() {
		t.Fatalf("cwd = %q; want %q", got[0], agentDir())
	}
	if sess, _, _ := store.Session("s1"); sess.VendorSessionID != "vend-1" {
		t.Fatalf("vendor session id = %q; want vend-1", sess.VendorSessionID)
	}
	if ms, _ := store.Messages("s1"); len(ms) != 2 || ms[0].Content != "do the thing" || ms[1].Content != "pong" {
		t.Fatalf("messages = %+v; want the exchange", ms)
	}

	// second turn resumes and refreshes the id (claude forks per turn);
	// re-read the row like the queue worker does — fresh vendor id
	sess, _, _ = store.Session("s1")
	srv.agentAnswer(context.Background(), sess, "again")
	got := readLines(t, argsFile)
	if i := slices.Index(got, "--resume"); i < 0 || got[i+1] != "vend-1" {
		t.Fatalf("resume args = %q; want --resume vend-1", got)
	}
	if sess, _, _ := store.Session("s1"); sess.VendorSessionID != "vend-r" {
		t.Fatalf("vendor session id after resume = %q; want vend-r", sess.VendorSessionID)
	}
}

func TestAgentAnswerCodex(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	argsFile, _ := writeAgentFakes(t)
	store.AddSession("s1", true, "openai")
	store.SetActiveSession("s1")

	sess, _, _ := store.Session("s1")
	reply := srv.agentAnswer(context.Background(), sess, "do the thing")
	if reply != "pong" {
		t.Fatalf("agent reply = %q; want pong", reply)
	}
	got := readLines(t, argsFile)
	// the -o value is a temp file; drop the pair before comparing
	if i := slices.Index(got, "-o"); i < 0 {
		t.Fatalf("codex args = %q; want a -o last-message file", got)
	} else {
		got = slices.Delete(got, i, i+2)
	}
	want := []string{"exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "--json", "do the thing"}
	if !slices.Equal(got, want) {
		t.Fatalf("codex args = %q; want %q", got, want)
	}
	if sess, _, _ := store.Session("s1"); sess.VendorSessionID != "vend-1" {
		t.Fatalf("vendor session id = %q; want vend-1", sess.VendorSessionID)
	}

	// resume goes through the exec resume subcommand
	sess, _, _ = store.Session("s1")
	srv.agentAnswer(context.Background(), sess, "again")
	got = readLines(t, argsFile)
	if got[0] != "exec" || got[1] != "resume" || got[2] != "vend-1" {
		t.Fatalf("codex resume args = %q; want exec resume vend-1 …", got)
	}
	if slices.Contains(got, "--ignore-user-config") || slices.Contains(got, "read-only") {
		t.Fatalf("codex agent args carry bare-mode flags: %q", got)
	}
}

// TestAgentAnswerSendFile: a TOOL:send_file line in the agent reply uploads
// the file and the stored history keeps the 📎 confirmation, never the line.
func TestAgentAnswerSendFile(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store.AddPairing("telegram", "42", "CODE")
	store.ApprovePairing("telegram", "CODE")
	calls := make(chan map[string]any, 8)
	fake := fakeMediaAPI(t, calls)
	srv.tg = NewTelegram(fake.URL, "TOKEN")

	payload := filepath.Join(t.TempDir(), "out.pdf")
	if err := os.WriteFile(payload, []byte("PDF"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	result := `{"result":"done\nTOOL:send_file {\"path\":\"` + payload + `\"}","session_id":"v1"}`
	// printf, not echo: dash's echo turns the \n JSON escape into a real newline
	script := "#!/bin/sh\nprintf '%s\\n' '" + result + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	store.AddSession("s1", true, "claude")
	store.SetActiveSession("s1")
	// past exchange so the background nameSession call never fires
	store.AddMessage("s1", "user", "seed", 1)
	store.AddMessage("s1", "assistant", "seeded", 2)

	sess, _, _ := store.Session("s1")
	reply := srv.agentAnswer(context.Background(), sess, "make the pdf")
	if reply != "done\n📎 sent out.pdf" {
		t.Fatalf("reply = %q; want done + 📎 confirmation", reply)
	}
	up := nextCall(t, calls, "sendDocument")
	if up["chat_id"] != "42" || up["filename"] != "out.pdf" || up["bytes"] != "PDF" {
		t.Fatalf("sendDocument call = %v", up)
	}
	ms, _ := store.Messages("s1")
	last := ms[len(ms)-1].Content
	if strings.Contains(last, "TOOL:") || !strings.Contains(last, "📎 sent out.pdf") {
		t.Fatalf("stored assistant message = %q; want 📎, no TOOL line", last)
	}
}

// TestEnsureAgentDirAppendsContract: pre-existing workspaces get the
// send_file contract appended once, owner edits preserved.
func TestEnsureAgentDirAppendsContract(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := agentDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	owned := "# my custom notes\ndo not lose this\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(owned), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureAgentDir(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if !strings.HasPrefix(string(raw), owned) || !strings.Contains(string(raw), "TOOL:send_file") {
		t.Fatalf("AGENTS.md = %q; want owner text kept + contract appended", raw)
	}
	if err := ensureAgentDir(); err != nil { // idempotent
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(again) != string(raw) {
		t.Fatal("second ensureAgentDir changed AGENTS.md; want no-op")
	}
	// the absent file was seeded fresh — the seed already carries the contract
	seeded, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(seeded), "TOOL:send_file") {
		t.Fatal("fresh seed missing the send_file contract")
	}
}

// TestAgentAnswerPersonality: with personality set, the style marker rides
// the wire to the vendor CLI but never lands in the stored history.
func TestAgentAnswerPersonality(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	argsFile, _ := writeAgentFakes(t)
	if err := os.MkdirAll(filepath.Dir(ConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(), []byte("personality: quiet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.AddSession("s1", true, "claude")
	store.SetActiveSession("s1")
	store.AddMessage("s1", "user", "seed", 1) // no background nameSession
	store.AddMessage("s1", "assistant", "seeded", 2)

	sess, _, _ := store.Session("s1")
	srv.agentAnswer(context.Background(), sess, "do the thing")
	args := strings.Join(readLines(t, argsFile), "\n")
	if !strings.Contains(args, "quiet mode:") || !strings.Contains(args, "do the thing") {
		t.Fatalf("claude args = %q; want task text + quiet marker", args)
	}
	ms, _ := store.Messages("s1")
	if user := ms[len(ms)-2].Content; user != "do the thing" {
		t.Fatalf("stored user turn = %q; want the owner's words only", user)
	}
}

// TestAgentAnswerClaudeError: a claude result with is_error must surface as
// an error notice, not as a normal reply.
func TestAgentAnswerClaudeError(t *testing.T) {
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir()
	script := "#!/bin/sh\necho '{\"result\":\"boom\",\"session_id\":\"v\",\"is_error\":true}'\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	store.AddSession("s1", true, "claude")
	store.SetActiveSession("s1")

	sess, _, _ := store.Session("s1")
	reply := srv.agentAnswer(context.Background(), sess, "task")
	if !strings.HasPrefix(reply, "⚠ ") || !strings.Contains(reply, "boom") {
		t.Fatalf("error reply = %q; want ⚠ notice with the error text", reply)
	}
}
