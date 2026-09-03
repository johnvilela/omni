package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// taskReply renders a fake claude json result for text (\n JSON-escaped).
func taskReply(text string) string {
	return `{"result":"` + strings.ReplaceAll(text, "\n", `\n`) + `","session_id":"v"}`
}

// writeTaskFake installs a fake claude whose reply is selected by which task
// prompt it received — plan / worker / merge / plain step — so concurrent
// worker calls stay deterministic without a shared counter. Every received
// prompt is appended to the returned args file.
func writeTaskFake(t *testing.T, plan, step, merge, worker string) (argsFile string) {
	t.Helper()
	bin := t.TempDir()
	argsFile = filepath.Join(bin, "args.txt")
	script := `#!/bin/sh
printf '%s\n' "$2" >> ` + argsFile + `
case "$2" in
*"PLANNING step"*) printf '%s\n' '` + taskReply(plan) + `' ;;
*"one parallel worker"*) printf '%s\n' '` + taskReply(worker) + `' ;;
*"IS this step"*) printf '%s\n' '` + taskReply(merge) + `' ;;
*) printf '%s\n' '` + taskReply(step) + `' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return argsFile
}

// newTaskServer is an llm test server with an isolated agent workspace, one
// approved pairing (chat 42) and a recording telegram for notifications.
func newTaskServer(t *testing.T) (*Server, *Store, chan map[string]any) {
	t.Helper()
	srv, store := newLLMTestServer(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if err := store.AddPairing("telegram", "42", "CODE"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApprovePairing("telegram", "CODE"); err != nil {
		t.Fatal(err)
	}
	calls := make(chan map[string]any, 64)
	fake := fakeMediaAPI(t, calls)
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	return srv, store, calls
}

// waitTask polls until the task reaches status; the loop runs in background
// goroutines, so terminal assertions must wait.
func waitTask(t *testing.T, store *Store, id int64, status string) Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, ok, err := store.Task(id)
		if err == nil && ok && task.Status == status {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _, _ := store.Task(id)
	t.Fatalf("task %d status = %q; want %q", id, task.Status, status)
	return Task{}
}

// TestTaskHappyPath: /task <goal> plans, runs one step and finishes; the
// owner gets the DONE summary and the checkpoint folder exists.
func TestTaskHappyPath(t *testing.T) {
	srv, store, calls := newTaskServer(t)
	writeTaskFake(t, "planned\nCONTINUE wrote the plan", "did everything\nDONE all set", "DONE x", "DONE x")

	r := srv.handleTask("write a haiku")
	if !strings.Contains(r.Text, "task #1 started") {
		t.Fatalf("start reply = %q; want task #1 started", r.Text)
	}
	task := waitTask(t, store, 1, "done")
	if task.Note != "all set" || task.Step != 1 {
		t.Fatalf("task = %+v; want note 'all set', step 1", task)
	}
	if _, err := os.Stat(taskDir(1)); err != nil {
		t.Fatalf("checkpoint dir missing: %v", err)
	}
	msg := nextCall(t, calls, "sendMessage")
	if text, _ := msg["text"].(string); !strings.Contains(text, "✅ task #1 done — all set") {
		t.Fatalf("owner notification = %v; want ✅ done summary", msg)
	}
}

// TestTaskBlockedAndSteer: a BLOCKED step parks the task and asks the owner;
// /task #id <text> appends an owner note to the checkpoint and resumes.
func TestTaskBlockedAndSteer(t *testing.T) {
	srv, store, calls := newTaskServer(t)
	checkpoint := filepath.Join(taskDir(1), "task.md")
	bin := t.TempDir()
	script := `#!/bin/sh
PATH=/usr/bin:/bin # the test PATH holds only this fake; grep needs the real one
case "$2" in
*"PLANNING step"*) printf '%s\n' '` + taskReply("CONTINUE planned") + `' ;;
*) if grep -q March ` + checkpoint + ` 2>/dev/null; then
     printf '%s\n' '` + taskReply("DONE used March") + `'
   else
     printf '%s\n' '` + taskReply("BLOCKED which month?") + `'
   fi ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	srv.handleTask("sort my statements")
	task := waitTask(t, store, 1, "blocked")
	if task.Note != "which month?" {
		t.Fatalf("blocked note = %q; want the question", task.Note)
	}
	msg := nextCall(t, calls, "sendMessage")
	text, _ := msg["text"].(string)
	if !strings.Contains(text, "⛔ task #1 needs you: which month?") || !strings.Contains(text, "/task #1") {
		t.Fatalf("blocked notification = %q; want question + /task hint", text)
	}

	r := srv.handleTask("#1 March please")
	if !strings.Contains(r.Text, "resumed") {
		t.Fatalf("steer reply = %q; want resumed", r.Text)
	}
	raw, err := os.ReadFile(checkpoint)
	if err != nil || !strings.Contains(string(raw), "## Owner note") || !strings.Contains(string(raw), "March please") {
		t.Fatalf("checkpoint = %q (%v); want the owner note appended", raw, err)
	}
	task = waitTask(t, store, 1, "done")
	if task.Note != "used March" {
		t.Fatalf("done note = %q; want 'used March'", task.Note)
	}
}

// TestTaskFanout: a FANOUT step runs clamped parallel workers, the server
// backfills their results files, the next step merges and the scratch dirs
// are cleaned up.
func TestTaskFanout(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	argsFile := writeTaskFake(t,
		"CONTINUE planned",
		"FANOUT 99", // clamped to maxFanout
		"folded results\nDONE merged",
		"did my item")

	srv.handleTask("analyze the statements")
	task := waitTask(t, store, 1, "done")
	if task.Note != "merged" || task.Step != 2 { // plan + fan-out; DONE doesn't bump
		t.Fatalf("task = %+v; want note merged, step 2", task)
	}
	raw, _ := os.ReadFile(argsFile)
	if n := strings.Count(string(raw), "one parallel worker"); n != maxFanout {
		t.Fatalf("worker runs = %d; want clamp to %d", n, maxFanout)
	}
	// server backfilled results (the fake never writes files), then cleaned up
	if _, err := os.Stat(filepath.Join(taskDir(1), "results")); !os.IsNotExist(err) {
		t.Fatalf("results dir survived the merge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(taskDir(1), "items")); !os.IsNotExist(err) {
		t.Fatalf("items dir survived the fan-out: %v", err)
	}
}

// TestTaskStrikes: an agent that stops emitting control lines fails the task
// after two consecutive misses instead of looping forever.
func TestTaskStrikes(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	writeTaskFake(t, "CONTINUE planned", "just chatting, no protocol", "x", "x")

	srv.handleTask("goal")
	task := waitTask(t, store, 1, "failed")
	if !strings.Contains(task.Note, "control line") {
		t.Fatalf("fail note = %q; want the control-line reason", task.Note)
	}
}

// TestTaskErrorRetry: one CLI failure retries the same iteration; the retry
// succeeding keeps the task alive.
func TestTaskErrorRetry(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	bin := t.TempDir()
	marker := filepath.Join(bin, "failed-once")
	script := `#!/bin/sh
case "$2" in
*"PLANNING step"*) printf '%s\n' '` + taskReply("CONTINUE planned") + `' ;;
*) if [ ! -f ` + marker + ` ]; then : > ` + marker + `; exit 1; fi
   printf '%s\n' '` + taskReply("DONE recovered") + `' ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	srv.handleTask("goal")
	task := waitTask(t, store, 1, "done")
	if task.Note != "recovered" {
		t.Fatalf("note = %q; want recovered after one retry", task.Note)
	}
}

// TestTaskErrorTwiceFails: two consecutive CLI failures end the task with the
// error surfaced.
func TestTaskErrorTwiceFails(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	srv.handleTask("goal")
	task := waitTask(t, store, 1, "failed")
	if task.Note == "" {
		t.Fatal("fail note empty; want the CLI error")
	}
}

// TestTaskStepCap: a task at the iteration cap fails immediately — the
// runaway guard survives restarts because step is persisted.
func TestTaskStepCap(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	if _, err := store.AddTask("runaway"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE tasks SET step = ?`, maxTaskSteps); err != nil {
		t.Fatal(err)
	}
	srv.runTaskLoop(1)
	task, _, _ := store.Task(1)
	if task.Status != "failed" || !strings.Contains(task.Note, "step cap") {
		t.Fatalf("task = %+v; want failed on the step cap", task)
	}
}

// TestResumeTasks: boot resume restarts only tasks left running; finished
// ones stay put.
func TestResumeTasks(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	argsFile := writeTaskFake(t, "nothing left\nDONE quick", "DONE quick", "x", "x")
	store.AddTask("interrupted") // status running, like a crash mid-task
	store.AddTask("old")
	store.SetTaskStatus(2, "done", "finished long ago")

	srv.resumeTasks()
	waitTask(t, store, 1, "done")
	if task, _, _ := store.Task(2); task.Note != "finished long ago" {
		t.Fatalf("finished task touched: %+v", task)
	}
	if raw, _ := os.ReadFile(argsFile); strings.Count(string(raw), "long task #") != 1 {
		t.Fatalf("agent runs = %q; want exactly one (task #1 only)", raw)
	}
}

// TestTaskCallbacks: the /tasks buttons drive the status machine through
// gatedCallback and re-render the listing in place.
func TestTaskCallbacks(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	writeTaskFake(t, "DONE finished", "DONE finished", "x", "x")
	store.AddTask("goal") // running row without a live loop

	r := srv.gatedCallback(context.Background(), 42, "task-pause:1")
	if !r.Edit || !strings.Contains(r.Text, "⏸ #1 · paused") {
		t.Fatalf("pause reply = %+v; want edited listing with paused row", r)
	}
	r = srv.gatedCallback(context.Background(), 42, "task-resume:1")
	if !r.Edit {
		t.Fatalf("resume reply = %+v; want edited listing", r)
	}
	task := waitTask(t, store, 1, "done") // resumed loop ran to completion
	r = srv.gatedCallback(context.Background(), 42, "task-cancel:1")
	if task, _, _ = store.Task(1); task.Status != "done" {
		t.Fatalf("cancel touched a finished task: %+v", task)
	}
	if !r.Edit || !strings.Contains(r.Text, "✅ #1 · done") {
		t.Fatalf("stale cancel reply = %+v; want healed listing", r)
	}
}

// TestTaskStartTool: the chat TOOL line starts a task and is gated by owner
// approval; agent replies get the same tool via applyAgentTools.
func TestTaskStartTool(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	writeTaskFake(t, "DONE ok", "DONE ok", "x", "x")

	if names := gatedNames("TOOL:task_start {\"goal\":\"x\"}"); len(names) != 1 || names[0] != "task_start" {
		t.Fatalf("gatedNames = %q; want task_start gated", names)
	}
	out := srv.runTool(context.Background(), "task_start", `{"goal":"from chat"}`)
	if !strings.Contains(out, "⚙ task #1 started") {
		t.Fatalf("runTool = %q; want start confirmation", out)
	}
	out = srv.applyAgentTools(context.Background(), "handing off\nTOOL:task_start {\"goal\":\"from agent\"}")
	if strings.Contains(out, "TOOL:") || !strings.Contains(out, "⚙ task #2 started") {
		t.Fatalf("applyAgentTools = %q; want confirmation, no TOOL line", out)
	}
	waitTask(t, store, 1, "done")
	waitTask(t, store, 2, "done")
	if out := srv.runTool(context.Background(), "task_start", `{"goal":""}`); !strings.Contains(out, "⚠") {
		t.Fatalf("empty goal = %q; want refusal", out)
	}
}

// TestParseControl locks the last-non-empty-line protocol.
func TestParseControl(t *testing.T) {
	for _, tc := range []struct{ reply, verb, rest string }{
		{"did stuff\nCONTINUE next is step 3\n\n", "CONTINUE", "next is step 3"},
		{"DONE all done", "DONE", "all done"},
		{"notes\nBLOCKED need the password", "BLOCKED", "need the password"},
		{"briefs written\nFANOUT 4", "FANOUT", "4"},
		{"the contract says end with CONTINUE\nbut I forgot", "", ""},
		{"", "", ""},
		{"DONE early\ntrailing chatter", "", ""}, // only the LAST line counts
	} {
		verb, rest := parseControl(tc.reply)
		if verb != tc.verb || rest != tc.rest {
			t.Errorf("parseControl(%q) = %q %q; want %q %q", tc.reply, verb, rest, tc.verb, tc.rest)
		}
	}
}

// TestListTasks: the /tasks rendering — live tasks with buttons, blocked
// question inline, /task usage and status views.
func TestListTasks(t *testing.T) {
	srv, store, _ := newTaskServer(t)
	if r := srv.listTasks(); !strings.Contains(r.Text, "no tasks yet") {
		t.Fatalf("empty listing = %q", r.Text)
	}
	store.AddTask("first goal")
	store.AddTask("second goal")
	store.SetTaskStatus(2, "blocked", "need input")

	r := srv.listTasks()
	if !strings.Contains(r.Text, "⚙ #1 · running") || !strings.Contains(r.Text, "⛔ #2 · blocked") ||
		!strings.Contains(r.Text, "need input") {
		t.Fatalf("listing = %q; want both rows + blocked question", r.Text)
	}
	if len(r.Keyboard) != 2 || len(r.Keyboard[0]) != 2 {
		t.Fatalf("keyboard = %+v; want a 2-button row per live task", r.Keyboard)
	}
	if r := srv.handleTask(""); !strings.Contains(r.Text, "usage") {
		t.Fatalf("bare /task = %q; want usage", r.Text)
	}
	if r := srv.handleTask("#2"); !strings.Contains(r.Text, "blocked") || !strings.Contains(r.Text, "need input") {
		t.Fatalf("/task #2 = %q; want status + note", r.Text)
	}
	if r := srv.handleTask("#9"); !strings.Contains(r.Text, "not found") {
		t.Fatalf("/task #9 = %q; want not found", r.Text)
	}
}

// TestTaskStore locks the tasks table round-trip.
func TestTaskStore(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "omni.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.AddTask("do the thing")
	if err != nil || id != 1 {
		t.Fatalf("AddTask = %d, %v", id, err)
	}
	task, ok, err := store.Task(1)
	if err != nil || !ok || task.Goal != "do the thing" || task.Status != "running" || task.Step != 0 {
		t.Fatalf("Task = %+v, %v, %v", task, ok, err)
	}
	if err := store.BumpTaskStep(1, "step one done"); err != nil {
		t.Fatal(err)
	}
	if task, _, _ = store.Task(1); task.Step != 1 || task.Note != "step one done" {
		t.Fatalf("after bump = %+v", task)
	}
	if err := store.SetTaskStatus(1, "done", "summary"); err != nil {
		t.Fatal(err)
	}
	store.AddTask("second")
	ts, err := store.Tasks()
	if err != nil || len(ts) != 2 || ts[0].ID != 2 || ts[1].Status != "done" {
		t.Fatalf("Tasks = %+v, %v; want newest first", ts, err)
	}
	if _, ok, _ := store.Task(9); ok {
		t.Fatal("Task(9) = ok; want missing")
	}
}
