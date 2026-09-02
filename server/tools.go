package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// cronPrompt is the scheduled-jobs section injected into every chat prompt:
// the live job list (so listing and suggesting need no tool round) plus the
// mutation contract. omni's first tool protocol.
func cronPrompt(store *Store) string {
	var b strings.Builder
	b.WriteString("## Scheduled jobs\n\nCurrent jobs:\n")
	cs, err := store.Crons()
	if err != nil || len(cs) == 0 {
		b.WriteString("none yet\n")
	}
	for _, c := range cs {
		fmt.Fprintf(&b, "#%d · %s · %s · %s\n", c.ID, c.Schedule, c.Kind, c.Text)
	}
	b.WriteString(`
To create, change or delete a job, reply with a line in this exact form
(each on its own line, nothing else on it):
TOOL:cron_add {"schedule":"0 8 * * *","kind":"message","text":"..."}
TOOL:cron_edit {"id":3,"schedule":"...","kind":"...","text":"..."}
TOOL:cron_delete {"id":3}

Schedules are standard 5-field cron (min hour dom month dow), server local
time. Kinds: "message" = the text is sent to the owner as-is; "prompt" = the
text is answered by you and the answer is sent; "agent" = the text runs as an
autonomous task on the owner's PC (tools, browser) and the result is sent.
When the owner asks for a reminder or a recurring action, create a job. When
a message implies a recurring need, offer to create one first.`)
	return b.String()
}

// applyTools executes TOOL: lines in an llm chat reply, replacing each with
// a deterministic confirmation; everything else passes through. Single-pass:
// the model sees the confirmations in history on the next turn, not the
// results in this one.
func (s *Server) applyTools(reply string) string {
	if !strings.Contains(reply, "TOOL:") {
		return reply
	}
	lines := strings.Split(reply, "\n")
	for i, line := range lines {
		name, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(name, "TOOL:") {
			continue
		}
		lines[i] = s.runTool(strings.TrimPrefix(name, "TOOL:"), args)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (s *Server) runTool(name, args string) string {
	var c struct {
		ID                   int64
		Schedule, Kind, Text string
	}
	if err := json.Unmarshal([]byte(args), &c); err != nil {
		return "⚠ bad tool arguments: " + err.Error()
	}
	validate := func() string {
		if _, err := parseCron(c.Schedule); err != nil {
			return fmt.Sprintf("⚠ invalid schedule %q: %v", c.Schedule, err)
		}
		if c.Kind != "message" && c.Kind != "prompt" && c.Kind != "agent" {
			return fmt.Sprintf("⚠ invalid kind %q — use message, prompt or agent", c.Kind)
		}
		return ""
	}
	switch name {
	case "cron_add":
		if msg := validate(); msg != "" {
			return msg
		}
		id, err := s.store.AddCron(c.Schedule, c.Kind, c.Text)
		if err != nil {
			return "⚠ " + err.Error()
		}
		return fmt.Sprintf("⏰ #%d created — %s · %s · %s", id, c.Schedule, c.Kind, c.Text)
	case "cron_edit":
		if msg := validate(); msg != "" {
			return msg
		}
		ok, err := s.store.UpdateCron(c.ID, c.Schedule, c.Kind, c.Text)
		if err != nil {
			return "⚠ " + err.Error()
		}
		if !ok {
			return fmt.Sprintf("⚠ job #%d not found", c.ID)
		}
		return fmt.Sprintf("⏰ #%d updated — %s · %s · %s", c.ID, c.Schedule, c.Kind, c.Text)
	case "cron_delete":
		ok, err := s.store.DeleteCron(c.ID)
		if err != nil {
			return "⚠ " + err.Error()
		}
		if !ok {
			return fmt.Sprintf("⚠ job #%d not found", c.ID)
		}
		return fmt.Sprintf("🗑 #%d removed", c.ID)
	}
	return "⚠ unknown tool " + name
}
