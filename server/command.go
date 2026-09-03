package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// handleMessage dispatches one approved chat message: slash commands first,
// everything else to the active session's answer path.
func (s *Server) handleMessage(ctx context.Context, text string) tgReply {
	text = strings.TrimSpace(text)
	if rest, ok := strings.CutPrefix(text, "!"); ok {
		return s.preempt(strings.TrimSpace(rest))
	}
	cmd, arg, _ := strings.Cut(text, " ")
	switch cmd {
	case "/new":
		if _, err := s.newSession(false, ""); err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		return tgReply{Text: "started a new session"}
	case "/agent":
		provider, note := agentProvider()
		if pick, rest, ok := cutAtProvider(arg); ok {
			p, err := parseProvider(pick)
			if err != nil {
				return tgReply{Text: "⚠ " + err.Error()}
			}
			if p == "gemini" {
				return tgReply{Text: "⚠ gemini is not supported for agent mode — use @openai or @claude"}
			}
			provider, note, arg = p, "", rest // explicit pick: no fallback note
		}
		sess, err := s.newSession(true, provider)
		if err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		if err := ensureAgentDir(); err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		if arg == "" {
			return tgReply{Text: note + "agent session started (" + provider + ") — send it a task"}
		}
		s.enqueue(sess.ID, arg)
		return tgReply{Text: note + "agent session started (" + provider + ") — ⏳ running"}
	case "/task":
		return s.handleTask(arg)
	case "/tasks":
		return s.listTasks()
	case "/sessions":
		return s.listSessions()
	case "/usage":
		return s.listUsage(ctx)
	case "/context":
		return s.showContext()
	case "/crons":
		return s.listCrons()
	case "/pin":
		return s.handlePin(ctx, arg)
	}
	if pick, rest, ok := cutAtProvider(text); ok {
		return s.atProvider(pick, rest)
	}
	return s.sessionAnswer(text)
}

// cutAtProvider splits a leading "@name" token off text.
func cutAtProvider(text string) (name, rest string, ok bool) {
	if !strings.HasPrefix(text, "@") {
		return "", text, false
	}
	name, rest, _ = strings.Cut(text[1:], " ")
	return name, strings.TrimSpace(rest), true
}

// parseProvider validates an @pick against the known providers.
func parseProvider(name string) (string, error) {
	if slices.Contains(llmProviders, name) {
		return name, nil
	}
	return "", fmt.Errorf("unknown provider %q — use @openai, @claude or @gemini", name)
}

// atProvider handles a sticky chat @pick: the active session is pinned to
// that provider until /new or another pick. Validation comes before any
// state change or saved message.
func (s *Server) atProvider(name, rest string) tgReply {
	provider, err := parseProvider(name)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if sess.Agent {
		return tgReply{Text: "⚠ an agent session keeps its provider — start a new one with /agent @" + provider}
	}
	if err := s.store.SetSessionProvider(sess.ID, provider); err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if rest == "" {
		return tgReply{Text: "session now uses " + provider}
	}
	return s.sessionAnswer(rest)
}

// newSession creates a session and points the active pointer at it.
func (s *Server) newSession(agent bool, provider string) (Session, error) {
	id := uuid.Must(uuid.NewV7()).String()
	if err := s.store.AddSession(id, agent, provider); err != nil {
		return Session{}, err
	}
	if err := s.store.SetActiveSession(id); err != nil {
		return Session{}, err
	}
	go s.refreshPin()
	return Session{ID: id, Agent: agent, Provider: provider}, nil
}

// agentProvider picks the vendor CLI for a new agent session: the default
// llm when it can act as an agent, else claude. Gemini has no usable
// non-interactive session resume, so it falls back with a note.
func agentProvider() (provider, note string) {
	switch readConfig().DefaultLLM {
	case "openai":
		return "openai", ""
	case "gemini":
		return "claude", "gemini is not supported for agent mode — using claude\n\n"
	}
	return "claude", ""
}

// listSessions renders the last 5 sessions as an inline keyboard; tapping a
// button resumes that session (handled by gatedCallback).
func (s *Server) listSessions() tgReply {
	rs, err := s.store.RecentSessions(5)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if len(rs) == 0 {
		return tgReply{Text: "no sessions yet — just send a message"}
	}
	var kb [][]button
	for _, r := range rs {
		label := recentLabel(r)
		if r.Unread != "" {
			label = "✉ " + label
		}
		if s.running(r.ID) {
			label = "⏳ " + label
		}
		kb = append(kb, []button{{Text: label, CallbackData: r.ID}})
	}
	return tgReply{Text: "sessions — tap to resume:", Keyboard: kb}
}

// recentLabel is a listing row's display name: llm-given title, else the
// first message, truncated; 🤖 marks agent sessions.
func recentLabel(r RecentSession) string {
	label := r.Name
	if label == "" {
		label = r.FirstMsg
	}
	if label == "" {
		label = "(empty)"
	}
	if runes := []rune(label); len(runes) > 40 {
		label = string(runes[:40]) + "…"
	}
	if r.Agent {
		label = "🤖 " + label
	}
	return label
}

// listCrons renders the scheduled jobs with a delete button per job.
func (s *Server) listCrons() tgReply {
	cs, err := s.store.Crons()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if len(cs) == 0 {
		return tgReply{Text: "no scheduled jobs — just ask for a reminder"}
	}
	var b strings.Builder
	var kb [][]button
	for _, c := range cs {
		fmt.Fprintf(&b, "#%d · %s · %s\n%s\n", c.ID, c.Schedule, c.Kind, c.Text)
		label := c.Text
		if runes := []rune(label); len(runes) > 30 {
			label = string(runes[:30]) + "…"
		}
		kb = append(kb, []button{{Text: fmt.Sprintf("🗑 #%d %s", c.ID, label), CallbackData: fmt.Sprintf("cron-del:%d", c.ID)}})
	}
	return tgReply{Text: strings.TrimSpace(b.String()), Keyboard: kb}
}

// deleteCronCallback handles a 🗑 button tap from /crons: the job is removed
// and the tapped listing refreshes in place — remaining buttons stay live, a
// stale tap just heals the outdated list.
func (s *Server) deleteCronCallback(data string) tgReply {
	id, err := strconv.ParseInt(data, 10, 64)
	if err != nil {
		return tgReply{Text: "⚠ bad job id"}
	}
	if _, err := s.store.DeleteCron(id); err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	r := s.listCrons()
	r.Edit = true
	return r
}

// resumeSession points the active pointer at an existing session and delivers
// any answer that finished while it wasn't active.
func (s *Server) resumeSession(id string) tgReply {
	sess, ok, err := s.store.Session(id)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if !ok {
		return tgReply{Text: "session not found"}
	}
	if err := s.store.SetActiveSession(id); err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	reply := "resumed: " + sessionLabel(sess)
	if u := strings.TrimSpace(sess.Unread); u != "" {
		s.store.ClearSessionUnread(sess.ID) // best-effort
		reply += "\n\n" + u
	}
	go s.refreshPin()
	return tgReply{Text: reply}
}

// sessionAnswer routes a plain message to the active session's background
// queue; the answer arrives later as a push (or as unread if the user has
// switched sessions by then). Empty reply = the poller sends nothing now.
func (s *Server) sessionAnswer(text string) tgReply {
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	s.enqueue(sess.ID, text)
	return tgReply{}
}
