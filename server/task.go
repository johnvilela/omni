package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Long checkpointed tasks: a deterministic loop drives fresh-context one-shot
// agent runs (the fireCron pattern) against a markdown checkpoint the agents
// themselves maintain — no step depends on model memory, and a crash resumes
// from the checkpoint. The tasks table holds orchestration state only.
const (
	maxTaskSteps  = 30 // ≤15min each; honest ceiling, typical task ≈10-20 steps
	maxFanout     = 8  // work items per FANOUT declaration
	maxTaskAgents = 4  // concurrent agent runs, all tasks + workers combined
)

// taskDir is one task's checkpoint folder inside the agent workspace:
// task.md (the checkpoint), items/ (fan-out briefs), results/ (worker output).
func taskDir(id int64) string {
	return filepath.Join(agentDir(), "tasks", strconv.FormatInt(id, 10))
}

// startTask inserts the row, creates the checkpoint folder and spawns the loop.
func (s *Server) startTask(goal string) (int64, error) {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return 0, fmt.Errorf("task: goal required")
	}
	id, err := s.store.AddTask(goal)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(taskDir(id), 0o700); err != nil {
		return 0, err
	}
	go s.runTaskLoop(id)
	return id, nil
}

// resumeTasks restarts the loop of every task left running by the previous
// server run; the checkpoint files carry the state, so re-running the
// interrupted step is safe. Blocked and paused tasks stay put.
func (s *Server) resumeTasks() {
	ts, err := s.store.Tasks()
	if err != nil {
		log.Printf("task: resume: %v", err)
		return
	}
	for _, t := range ts {
		if t.Status == "running" {
			go s.runTaskLoop(t.ID)
		}
	}
}

// runTaskLoop drives one task to a terminal status. One loop per task; each
// iteration re-reads the row (pause/cancel/steer settle between iterations)
// and runs one fresh-context agent step against the checkpoint. Own context
// like runTask — only the pause/cancel buttons cancel it.
// ponytail: a resume tapped within ms of pause can hit the dying loop's guard
// and no-op — tap resume again; a done-channel handshake fixes it if it bites.
func (s *Server) runTaskLoop(id int64) {
	s.taskMu.Lock()
	if _, alive := s.taskCancel[id]; alive {
		s.taskMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.taskCancel[id] = cancel
	s.taskMu.Unlock()
	defer func() {
		s.taskMu.Lock()
		delete(s.taskCancel, id)
		s.taskMu.Unlock()
		cancel()
		go s.refreshPin()
	}()
	go s.refreshPin()

	resDir := filepath.Join(taskDir(id), "results")
	errs, strikes := 0, 0
	for {
		t, ok, err := s.store.Task(id)
		if err != nil || !ok || t.Status != "running" {
			return
		}
		if t.Step >= maxTaskSteps {
			s.failTask(id, fmt.Sprintf("step cap (%d) reached", maxTaskSteps))
			return
		}
		merge := dirNonEmpty(resDir)
		prompt := stepPrompt(id, t.Goal, merge)
		if t.Step == 0 {
			prompt = planPrompt(id, t.Goal)
		}
		reply, err := s.runTaskAgent(ctx, prompt)
		if ctx.Err() != nil {
			return // paused/cancelled: the button handler already set the status
		}
		if err != nil {
			errs++
			if errs >= 2 {
				s.failTask(id, err.Error())
				return
			}
			continue // retry the same iteration once
		}
		errs = 0
		if merge {
			os.RemoveAll(resDir) // consumed: the merge step just saw them
		}
		verb, rest := parseControl(reply)
		n := 0
		if verb == "FANOUT" {
			nStr, _, _ := strings.Cut(rest, " ")
			if n, err = strconv.Atoi(nStr); err != nil || n < 1 {
				verb = "" // malformed fan-out counts as a missing control line
			}
			n = min(n, maxFanout)
		}
		if verb == "" {
			strikes++
			if strikes >= 2 {
				s.failTask(id, "agent stopped emitting control lines")
				return
			}
			s.store.BumpTaskStep(id, "(missing control line)")
			continue
		}
		strikes = 0
		switch verb {
		case "CONTINUE":
			s.store.BumpTaskStep(id, rest)
		case "FANOUT":
			s.runWorkers(ctx, id, n)
			if ctx.Err() != nil {
				return
			}
			s.store.BumpTaskStep(id, fmt.Sprintf("fan-out: %d workers", n))
		case "DONE":
			summary := s.applySendFile(ctx, rest) // send_file only — a step can never start sub-tasks
			s.store.SetTaskStatus(id, "done", summary)
			s.notifyOwner(context.Background(), tgReply{Text: fmt.Sprintf("✅ task #%d done — %s", id, summary)})
			return
		case "BLOCKED":
			s.store.SetTaskStatus(id, "blocked", rest)
			s.notifyOwner(context.Background(), tgReply{
				Text: fmt.Sprintf("⛔ task #%d needs you: %s\n\nanswer with: /task #%d <your answer>", id, rest, id),
				Keyboard: [][]button{{{Text: "✖ cancel task", CallbackData: fmt.Sprintf("task-cancel:%d", id)}}},
			})
			return
		}
		go s.refreshPin() // step count moved
	}
}

// failTask marks a task failed and tells the owner why.
func (s *Server) failTask(id int64, reason string) {
	s.store.SetTaskStatus(id, "failed", reason)
	s.notifyOwner(context.Background(), tgReply{Text: fmt.Sprintf("❌ task #%d failed — %s", id, reason)})
}

// runTaskAgent runs one fresh-context one-shot agent turn under the global
// semaphore — the cap covers steps and workers across every task, so two
// tasks can never stack more than maxTaskAgents vendor CLIs.
func (s *Server) runTaskAgent(ctx context.Context, prompt string) (string, error) {
	select {
	case s.agentSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.agentSem }()
	if err := ensureAgentDir(); err != nil {
		return "", err
	}
	provider, _ := agentProvider()
	var reply string
	var u callUsage
	var err error
	if provider == "openai" {
		reply, _, u, err = runCodexAgent(ctx, "", prompt)
	} else {
		reply, _, u, err = runClaudeAgent(ctx, "", prompt)
	}
	if err != nil {
		return "", err
	}
	s.recordUsage(provider, u)
	return reply, nil
}

// runWorkers runs one fan-out: n one-shot workers (semaphore-capped), each
// doing one items/<k>.md brief. A worker that errors or forgets its results
// file gets one written by the server, so the merge step always sees n files.
func (s *Server) runWorkers(ctx context.Context, id int64, n int) {
	resDir := filepath.Join(taskDir(id), "results")
	if err := os.MkdirAll(resDir, 0o700); err != nil {
		log.Printf("task %d: %v", id, err)
		return
	}
	var wg sync.WaitGroup
	for k := 1; k <= n; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			s.taskMu.Lock()
			s.taskWorkers[id]++
			s.taskMu.Unlock()
			go s.refreshPin()
			defer func() {
				s.taskMu.Lock()
				if s.taskWorkers[id]--; s.taskWorkers[id] == 0 {
					delete(s.taskWorkers, id)
				}
				s.taskMu.Unlock()
				go s.refreshPin()
			}()
			reply, err := s.runTaskAgent(ctx, workerPrompt(id, k))
			if ctx.Err() != nil {
				return // cancelled workers leave no result; the merge re-declares
			}
			res := filepath.Join(resDir, fmt.Sprintf("%d.md", k))
			if err != nil {
				os.WriteFile(res, []byte("⚠ worker failed: "+err.Error()), 0o644)
				return
			}
			if _, err := os.Stat(res); err != nil { // worker forgot its file
				os.WriteFile(res, []byte(reply), 0o644)
			}
		}(k)
	}
	wg.Wait()
	if ctx.Err() == nil {
		os.RemoveAll(filepath.Join(taskDir(id), "items")) // briefs consumed
	}
}

// controlVerbs are the loop's step-agent protocol; parseControl reads the
// last non-empty line only — scanning the whole reply would false-positive on
// replies that quote the contract.
var controlVerbs = []string{"CONTINUE", "DONE", "BLOCKED", "FANOUT"}

// parseControl reads the control line a step agent must end with; "" verb
// means the agent forgot the contract (a strike in the loop).
func parseControl(reply string) (verb, rest string) {
	lines := strings.Split(reply, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		v, r, _ := strings.Cut(line, " ")
		if slices.Contains(controlVerbs, v) {
			return v, strings.TrimSpace(r)
		}
		return "", ""
	}
	return "", ""
}

func dirNonEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

// handleTask is the /task command: "/task <goal>" starts a task, "/task #3"
// shows one, "/task #3 <text>" appends an owner note to the checkpoint and
// resumes it when blocked or paused — one mechanism for answering BLOCKED
// questions and steering mid-flight.
func (s *Server) handleTask(arg string) tgReply {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return tgReply{Text: "usage:\n/task <goal> — start a long task\n/task #<id> — show one\n/task #<id> <text> — answer or steer one\n/tasks — list all"}
	}
	if !strings.HasPrefix(arg, "#") {
		id, err := s.startTask(arg)
		if err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		return tgReply{Text: fmt.Sprintf("⚙ task #%d started — /tasks to follow", id)}
	}
	idStr, text, _ := strings.Cut(arg[1:], " ")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return tgReply{Text: "⚠ bad task id"}
	}
	t, ok, err := s.store.Task(id)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if !ok {
		return tgReply{Text: fmt.Sprintf("⚠ task #%d not found", id)}
	}
	text = strings.TrimSpace(text)
	if text == "" { // status view
		reply := taskLine(t, s.workerCount(t.ID))
		if t.Note != "" {
			reply += "\n" + t.Note
		}
		return tgReply{Text: reply}
	}
	note := fmt.Sprintf("\n\n## Owner note (%s)\n%s\n", time.Now().Format("2006-01-02 15:04"), text)
	f, err := os.OpenFile(filepath.Join(taskDir(id), "task.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	_, werr := f.WriteString(note)
	f.Close()
	if werr != nil {
		return tgReply{Text: "⚠ " + werr.Error()}
	}
	switch t.Status {
	case "blocked", "paused":
		if err := s.store.SetTaskStatus(id, "running", t.Note); err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		go s.runTaskLoop(id)
		return tgReply{Text: fmt.Sprintf("▶ task #%d resumed with your note", id)}
	case "running":
		return tgReply{Text: "📝 noted — the next step will see it"}
	}
	return tgReply{Text: fmt.Sprintf("⚠ task #%d is %s — its note was appended, but start a new task with /task", id, t.Status)}
}

// taskLine is one task's display row, shared by /tasks, /task #id and the
// pin dashboard.
func taskLine(t Task, workers int) string {
	icons := map[string]string{"running": "⚙", "paused": "⏸", "blocked": "⛔",
		"done": "✅", "failed": "❌", "cancelled": "🚫"}
	goal := t.Goal
	if runes := []rune(goal); len(runes) > 40 {
		goal = string(runes[:40]) + "…"
	}
	line := fmt.Sprintf("%s #%d · %s · step %d · %s", icons[t.Status], t.ID, t.Status, t.Step, goal)
	if workers > 0 {
		line += fmt.Sprintf(" · %d workers", workers)
	}
	return line
}

func (s *Server) workerCount(id int64) int {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	return s.taskWorkers[id]
}

// taskTerminal reports whether a status is final.
func taskTerminal(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled"
}

// listTasks renders /tasks: every live task with pause/resume/cancel buttons,
// plus the newest finished ones for context.
func (s *Server) listTasks() tgReply {
	ts, err := s.store.Tasks()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if len(ts) == 0 {
		return tgReply{Text: "no tasks yet — start one with /task <goal>"}
	}
	var b strings.Builder
	var kb [][]button
	shown := 0
	for _, t := range ts {
		if taskTerminal(t.Status) && shown >= 10 {
			continue // live tasks always render; old finished ones drop off
		}
		shown++
		b.WriteString(taskLine(t, s.workerCount(t.ID)) + "\n")
		if t.Status == "blocked" && t.Note != "" {
			b.WriteString("   " + t.Note + "\n")
		}
		if taskTerminal(t.Status) {
			continue
		}
		id := strconv.FormatInt(t.ID, 10)
		row := []button{{Text: "✖ cancel #" + id, CallbackData: "task-cancel:" + id}}
		if t.Status == "running" {
			row = append([]button{{Text: "⏸ pause #" + id, CallbackData: "task-pause:" + id}}, row...)
		} else {
			row = append([]button{{Text: "▶ resume #" + id, CallbackData: "task-resume:" + id}}, row...)
		}
		kb = append(kb, row)
	}
	return tgReply{Text: strings.TrimSpace(b.String()), Keyboard: kb}
}

// taskCallback handles the /tasks inline buttons; the tapped listing
// re-renders in place like deleteCronCallback, so a stale tap heals the list.
func (s *Server) taskCallback(action, data string) tgReply {
	id, err := strconv.ParseInt(data, 10, 64)
	if err != nil {
		return tgReply{Text: "⚠ bad task id"}
	}
	t, ok, err := s.store.Task(id)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if !ok {
		return tgReply{Text: fmt.Sprintf("⚠ task #%d not found", id)}
	}
	switch action {
	case "pause":
		if t.Status == "running" {
			// status first, then cancel: the loop re-reads status between
			// iterations, and a killed in-flight run must not count as an error
			if err := s.store.SetTaskStatus(id, "paused", t.Note); err != nil {
				return tgReply{Text: "⚠ " + err.Error()}
			}
			s.cancelTaskLoop(id)
		}
	case "resume":
		if t.Status == "paused" || t.Status == "blocked" {
			if err := s.store.SetTaskStatus(id, "running", t.Note); err != nil {
				return tgReply{Text: "⚠ " + err.Error()}
			}
			go s.runTaskLoop(id)
		}
	case "cancel":
		if !taskTerminal(t.Status) {
			if err := s.store.SetTaskStatus(id, "cancelled", t.Note); err != nil {
				return tgReply{Text: "⚠ " + err.Error()}
			}
			s.cancelTaskLoop(id)
		}
	}
	go s.refreshPin()
	r := s.listTasks()
	r.Edit = true
	return r
}

func (s *Server) cancelTaskLoop(id int64) {
	s.taskMu.Lock()
	cancel := s.taskCancel[id]
	s.taskMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// liveTasks lists running + blocked tasks for the pin dashboard.
func (s *Server) liveTasks() ([]Task, error) {
	ts, err := s.store.Tasks()
	if err != nil {
		return nil, err
	}
	var live []Task
	for _, t := range ts {
		if t.Status == "running" || t.Status == "blocked" {
			live = append(live, t)
		}
	}
	return live, nil
}

// planPrompt is iteration 0: the agent writes the plan into the checkpoint
// and executes nothing.
func planPrompt(id int64, goal string) string {
	return fmt.Sprintf(`You are executing long task #%d for the owner. Goal:

%s

This is the PLANNING step. Create %s/task.md with: the goal, a numbered step
list (each step small enough for one ~10-minute agent run), and a Progress
section marking every step pending. Do not execute any step yet.
End your reply with exactly this on its own last line:
CONTINUE wrote the plan`, id, goal, taskDir(id))
}

// stepPrompt is every later iteration: read the checkpoint, do ONE step,
// update the checkpoint, end with a control line.
func stepPrompt(id int64, goal string, merge bool) string {
	dir := taskDir(id)
	mergeNote := ""
	if merge {
		mergeNote = fmt.Sprintf(`
Files under %s/results/ hold the results of the parallel workers you launched
last step. Merging them into task.md IS this step's work.
`, dir)
	}
	return fmt.Sprintf(`You are executing long task #%d. Goal:

%s

You have NO memory of previous steps. %s/task.md is the single source of
truth — read it FIRST. Owner notes appended at the bottom override the plan.
%s
Execute exactly ONE pending step — the next one — then update task.md: mark
it done and record what happened plus anything the next step needs. Never
claim progress you did not make.

To run up to %d independent work items in parallel instead: write one
self-contained brief per item to %s/items/1.md … n.md, note the fan-out in
task.md, and end with the FANOUT line. Workers will write %s/results/<k>.md;
you merge them next step.

The LAST line of your reply MUST be exactly one control line:
CONTINUE <short progress note>
DONE <summary for the owner>
BLOCKED <question for the owner>
FANOUT <n>`, id, goal, dir, mergeNote, maxFanout, dir, dir)
}

// workerPrompt is one fan-out worker's brief.
func workerPrompt(id int64, k int) string {
	dir := taskDir(id)
	return fmt.Sprintf(`You are one parallel worker on long task #%d. Read %s/task.md for context,
then do ONLY the work item in %s/items/%d.md. Write your full findings to
%s/results/%d.md. NEVER edit task.md or any other file under %s — other
workers run beside you. Reply with one short line when done.`,
		id, dir, dir, k, dir, k, dir)
}

// taskPrompt is the long-tasks section injected into every chat prompt: the
// live task list (status questions need no tool round) plus the start
// contract. Sibling of cronPrompt.
func taskPrompt(store *Store) string {
	var b strings.Builder
	b.WriteString("## Long tasks\n\nCurrent tasks:\n")
	ts, err := store.Tasks()
	live := 0
	for _, t := range ts {
		if taskTerminal(t.Status) {
			continue
		}
		fmt.Fprintf(&b, "#%d · %s · step %d · %s\n", t.ID, t.Status, t.Step, t.Goal)
		live++
	}
	if err != nil || live == 0 {
		b.WriteString("none running\n")
	}
	b.WriteString(`
To start a long multi-step background job (many autonomous agent runs over
hours, against a persistent checkpoint), reply with this line alone on its
own line:
TOOL:task_start {"goal":"..."}

Offer it when the owner asks for something clearly beyond one reply — bulk
analysis, research across many sources, multi-stage setup. Include EVERY
detail the owner gave in the goal: the task agents start with fresh context
and see nothing else from this chat. The owner follows progress with /tasks
and steers with /task #<id> <text>.`)
	return b.String()
}
