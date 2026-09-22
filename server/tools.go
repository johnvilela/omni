package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
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
// only read_file results feed back into the same turn, via the follow-up
// round in chatAnswer. sessionID scopes session-bound tools (memory_load);
// "" from session-less call sites.
func (s *Server) applyTools(ctx context.Context, sessionID, reply string) string {
	if !strings.Contains(reply, "TOOL:") {
		return reply
	}
	lines := strings.Split(reply, "\n")
	for i, line := range lines {
		name, args, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || !strings.HasPrefix(name, "TOOL:") {
			continue
		}
		lines[i] = s.runTool(ctx, sessionID, strings.TrimPrefix(name, "TOOL:"), args)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (s *Server) runTool(ctx context.Context, sessionID, name, args string) string {
	switch name {
	case "write_file", "read_file", "edit_file", "delete_file", "send_file", "analyze_file":
		return s.fileTool(ctx, name, args)
	case "task_start":
		var a struct{ Goal string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "⚠ bad tool arguments: " + err.Error()
		}
		id, err := s.startTask(a.Goal)
		if err != nil {
			return "⚠ " + err.Error()
		}
		return fmt.Sprintf("⚙ task #%d started — /tasks to follow", id)
	case "memory_save":
		var a struct{ Theme, Text string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "⚠ bad tool arguments: " + err.Error()
		}
		if s.aiMemory == nil || a.Text == "" {
			return "⚠ memory_save: ai-memory not running or empty text"
		}
		theme := coreTheme(a.Theme)
		if theme == "" {
			theme = "general"
		}
		if err := s.appendCore(ctx, theme, a.Text); err != nil {
			return "⚠ memory_save: " + err.Error()
		}
		return fmt.Sprintf("🧠 core memory saved under %s: %s", theme, a.Text)
	case "memory_load":
		var a struct{ Theme string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "⚠ bad tool arguments: " + err.Error()
		}
		if sessionID == "" {
			return "⚠ memory_load: chat sessions only"
		}
		theme := coreTheme(a.Theme)
		facts := s.readCoreTheme(ctx, theme)
		if facts == "" {
			return fmt.Sprintf("⚠ memory_load: no theme %q — themes: %s", a.Theme, strings.Join(s.coreThemes(ctx), ", "))
		}
		sess, ok, err := s.store.Session(sessionID)
		if err != nil || !ok {
			return "⚠ memory_load: session not found"
		}
		loaded := strings.Split(sess.Themes, ",")
		if !slices.Contains(loaded, theme) {
			themes := theme
			if sess.Themes != "" {
				themes = sess.Themes + "," + theme
			}
			if err := s.store.SetSessionThemes(sessionID, themes); err != nil {
				return "⚠ memory_load: " + err.Error()
			}
		}
		return fmt.Sprintf("🧠 %s memory loaded (%d facts) — active from the next message", theme, countFacts(facts))
	case "plan_save":
		var a struct {
			Title, Body string
			Tags        []string
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "⚠ bad tool arguments: " + err.Error()
		}
		if s.aiMemory == nil || a.Body == "" {
			return "⚠ plan_save: ai-memory not running or empty body"
		}
		slug := planSlug(a.Title)
		path := planPath(slug)
		tags := append([]string{"omni", "plan"}, a.Tags...)
		long := ""
		if slices.Contains(a.Tags, "long") {
			long = "\n#long"
		}
		page := fmt.Sprintf("# Omni saved plan: %s\n\nStatus: active\nCreated: %s%s\n\n%s\n",
			slug, time.Now().Format("2006-01-02"), long, strings.TrimSpace(a.Body))
		if err := s.aiMemory.writePage(ctx, path, page, tags, true); err != nil {
			return "⚠ plan_save: " + err.Error()
		}
		return fmt.Sprintf("📋 plan saved — %s/%s (start it anytime: just ask)", plansDir, slug)
	case "plan_start":
		var a struct{ Slug string }
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "⚠ bad tool arguments: " + err.Error()
		}
		if s.aiMemory == nil {
			return "⚠ plan_start: ai-memory not running"
		}
		slug := planSlug(a.Slug)
		path := planPath(slug)
		raw, err := s.aiMemory.readPage(ctx, path)
		if err != nil {
			return fmt.Sprintf("⚠ plan %s not found", slug)
		}
		done, long := planMeta(raw)
		if done {
			return fmt.Sprintf("⚠ plan %s is already done", slug)
		}
		if long {
			id, err := s.store.AddCron("0 9 * * *", "agent", dailyPlanPrompt(path))
			if err != nil {
				return "⚠ plan_start: " + err.Error()
			}
			return fmt.Sprintf("⏰ #%d — daily agent job for plan %s (09:00; change it via the scheduled jobs)", id, slug)
		}
		id, err := s.startTask("Execute ai-memory plan page " + path + ": use memory_read_page first, work through ## Steps, and use memory_write_page to keep ## Progress updated. Set `Status: done` when ## Target is reached.")
		if err != nil {
			return "⚠ plan_start: " + err.Error()
		}
		return fmt.Sprintf("⚙ task #%d started for plan %s — /tasks to follow", id, slug)
	}
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
