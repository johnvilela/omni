package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// handleMessage dispatches one approved chat message: slash commands first,
// everything else to the active session's answer path.
func (s *Server) handleMessage(ctx context.Context, text string) tgReply {
	cmd, arg, _ := strings.Cut(strings.TrimSpace(text), " ")
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
		return tgReply{Text: note + s.agentAnswer(ctx, sess, arg)}
	case "/sessions":
		return s.listSessions()
	}
	if pick, rest, ok := cutAtProvider(text); ok {
		return s.atProvider(ctx, pick, rest)
	}
	return s.sessionAnswer(ctx, text)
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
func (s *Server) atProvider(ctx context.Context, name, rest string) tgReply {
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
	return s.sessionAnswer(ctx, rest)
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
		kb = append(kb, []button{{Text: label, CallbackData: r.ID}})
	}
	return tgReply{Text: "sessions — tap to resume:", Keyboard: kb}
}

// resumeSession points the active pointer at an existing session.
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
	name := sess.Name
	if name == "" {
		name = sess.ID
		if len(name) > 8 {
			name = name[:8]
		}
	}
	if sess.Agent {
		name = "🤖 " + name
	}
	return tgReply{Text: "resumed: " + name}
}

// sessionAnswer routes a plain message to the active session: agent sessions
// continue their vendor CLI conversation, chat sessions take the
// composed-history path.
func (s *Server) sessionAnswer(ctx context.Context, text string) tgReply {
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if sess.Agent {
		return tgReply{Text: s.agentAnswer(ctx, sess, text)}
	}
	return tgReply{Text: s.answerNotice(ctx, text)}
}
