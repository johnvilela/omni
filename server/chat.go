package main

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Session is one conversation thread. Single owner, channel-agnostic: the
// active session is the one the pointer table names (newest wins when unset).
type Session struct {
	ID                string
	Name              string
	ConsolidatedUntil int64  // highest messages.id folded into long-term memory
	Agent             bool   // vendor CLI runs un-bare with full tool access
	Provider          string // agent sessions only: "claude" | "openai"
	VendorSessionID   string // agent sessions only: the CLI's session to --resume
}

// Message is one turn of a session.
type Message struct {
	ID        int64
	Role      string // "user" | "assistant"
	Content   string
	CreatedAt int64 // unix seconds
}

const defaultTokenBudget = 8000

// tokenBudget is the config token_budget; 0/absent means the default
// (readConfig returns a zero Config on any error, so the fallback lives here).
func tokenBudget() int {
	if b := readConfig().TokenBudget; b > 0 {
		return b
	}
	return defaultTokenBudget
}

// estTokens estimates tokens as bytes/4 — overcounts non-ASCII, which errs on
// the cheap side. Exact tokenizers deliberately rejected: no new dep, no
// extra round-trip, and the budget is a cost cap, not a context-window fit.
func estTokens(s string) int { return len(s) / 4 }

// composePrompt builds the one text prompt sent identically to every provider
// path: persona + long-term memory + the history that fits the budget + the
// new message. Persona, memory and the new message are always included whole;
// history is walked newest→oldest and the turns that don't fit are returned
// as dropped (chronological) for compaction into long-term memory.
func composePrompt(persona, memory string, history []Message, text string, budget int) (string, []Message) {
	if persona == "" && memory == "" && len(history) == 0 {
		return text, nil // fresh install behaves exactly like the old single-turn bot
	}
	remaining := budget - estTokens(persona) - estTokens(memory) - estTokens(text)
	keep := len(history) // index of the oldest turn that still fits
	for keep > 0 && estTokens(history[keep-1].Content) <= remaining {
		remaining -= estTokens(history[keep-1].Content)
		keep--
	}

	var b strings.Builder
	if persona != "" {
		b.WriteString(strings.TrimSpace(persona) + "\n\n")
	}
	if memory != "" {
		b.WriteString("Long-term memory about the user:\n" + memory + "\n\n")
	}
	if keep < len(history) {
		b.WriteString("Conversation so far:\n")
		for _, m := range history[keep:] {
			b.WriteString(m.Role + ": " + m.Content + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("New user message (answer this):\n" + text)
	if keep == 0 {
		return b.String(), nil
	}
	return b.String(), history[:keep]
}

// ensureSession returns the active session, creating one when none exists.
// Sole session-creation point — becomes a hook event when omni grows hooks.
func (s *Server) ensureSession() (Session, error) {
	sess, ok, err := s.store.ActiveSession()
	if err != nil || ok {
		return sess, err
	}
	id := uuid.Must(uuid.NewV7()).String()
	if err := s.store.AddSession(id, false, ""); err != nil {
		return Session{}, err
	}
	if err := s.store.SetActiveSession(id); err != nil {
		return Session{}, err
	}
	return Session{ID: id}, nil
}

// ChatAnswer answers text within the active session: history within the
// token budget plus the long-term memory page, all composed into one text
// prompt for whichever provider path is the default.
func (s *Server) ChatAnswer(ctx context.Context, text string) (string, error) {
	sess, err := s.ensureSession()
	if err != nil {
		return "", err
	}
	history, err := s.store.Messages(sess.ID)
	if err != nil {
		return "", err
	}
	// save before asking: a failed llm call still keeps the user turn,
	// history stays truthful
	if _, err := s.store.AddMessage(sess.ID, "user", text, time.Now().Unix()); err != nil {
		return "", err
	}
	wiki := memoriaWiki()
	var memory string
	if wiki != "" {
		memory = readMemory(wiki)
	}
	prompt, dropped := composePrompt(readPersona(), memory, history, text, tokenBudget())
	reply, err := s.answerWith(ctx, sess.Provider, prompt)
	if err != nil {
		return "", err
	}
	if _, err := s.store.AddMessage(sess.ID, "assistant", reply, time.Now().Unix()); err != nil {
		return "", err
	}
	if len(history) == 0 {
		go s.nameSession(sess.ID, text)
	}
	var overflow []Message
	for _, m := range dropped {
		if m.ID > sess.ConsolidatedUntil {
			overflow = append(overflow, m)
		}
	}
	if wiki != "" && len(overflow) > 0 && s.digesting.CompareAndSwap(false, true) {
		go s.onCompaction(sess.ID, overflow)
	}
	return reply, nil
}

// nameSession asks the default llm to title the session; best-effort, any
// failure leaves the name empty (display falls back to the first message).
// Sole naming point — becomes a hook event when omni grows hooks.
func (s *Server) nameSession(id, firstMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	title, err := s.Answer(ctx, "Title this conversation in 3-5 words. Reply with the title only, no quotes.\n\n"+firstMsg)
	if err != nil {
		return
	}
	title, _, _ = strings.Cut(title, "\n")
	title = strings.Trim(title, `"' `)
	if len(title) > 60 {
		title = title[:60]
	}
	if title != "" {
		s.store.SetSessionName(id, title) // best-effort
	}
}
