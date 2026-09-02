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

	reply := srv.handleMessage(context.Background(), "do the thing")
	if reply.Text != "pong" {
		t.Fatalf("agent reply = %q; want pong", reply.Text)
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

	// second turn resumes and refreshes the id (claude forks per turn)
	srv.handleMessage(context.Background(), "again")
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

	reply := srv.handleMessage(context.Background(), "do the thing")
	if reply.Text != "pong" {
		t.Fatalf("agent reply = %q; want pong", reply.Text)
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
	srv.handleMessage(context.Background(), "again")
	got = readLines(t, argsFile)
	if got[0] != "exec" || got[1] != "resume" || got[2] != "vend-1" {
		t.Fatalf("codex resume args = %q; want exec resume vend-1 …", got)
	}
	if slices.Contains(got, "--ignore-user-config") || slices.Contains(got, "read-only") {
		t.Fatalf("codex agent args carry bare-mode flags: %q", got)
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

	reply := srv.handleMessage(context.Background(), "task")
	if !strings.HasPrefix(reply.Text, "⚠ ") || !strings.Contains(reply.Text, "boom") {
		t.Fatalf("error reply = %q; want ⚠ notice with the error text", reply.Text)
	}
}
