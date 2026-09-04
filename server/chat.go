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
	LastCtx           int64  // agent sessions only: context tokens reported on the last turn
	Unread            string // answers finished while another session was active; delivered on resume
	Plan              bool   // /plan interview in progress: planContract rides the prompt
	Themes            string // comma-joined core-memory themes loaded into this session
}

// Message is one turn of a session.
type Message struct {
	ID        int64
	Role      string // "user" | "assistant"
	Content   string
	CreatedAt int64 // unix seconds
}

// defaultTokenBudget is a cost cap, not a context-window fit: the whole
// composed prompt is re-sent uncached every turn, so a full budget costs
// budget × price-per-token on every message. Models with smaller windows are
// clamped by chatBudget; go lower via token_budget if api spend or oauth
// quota burn matters.
const defaultTokenBudget = 500_000

// modelWindows maps known chat models to their context window, matched by
// longest prefix so dated suffixes keep working.
var modelWindows = map[string]int64{
	"claude-fable":    1_000_000,
	"claude-opus-5":   1_000_000,
	"claude-sonnet-5": 1_000_000,
	"claude":          200_000,
	"gpt-5":           272_000,
	"gpt-4.1":         1_000_000,
	"gpt-4o":          128_000,
	"gemini":          1_000_000,
}

// modelWindow is the model's context window; unknown models get the smallest
// known window — a conservative clamp is safe, an optimistic one 400-errors
// mid-conversation.
func modelWindow(model string) int64 {
	best, w := 0, int64(128_000)
	for p, win := range modelWindows {
		if strings.HasPrefix(model, p) && len(p) > best {
			best, w = len(p), win
		}
	}
	return w
}

// chatModel is the model a chat call on provider would use: the configured
// pick, else the hardcoded cheap default. The oauth CLI path actually uses
// the CLI's own default — unknowable from here, so the clamp stays
// conservative until a model is configured explicitly.
func chatModel(provider string) string {
	if m := configuredModel(provider); m != "" {
		return m
	}
	return llmModels[provider]
}

// chatBudget is the effective compose budget for one provider's chat model:
// token_budget (default 500k) clamped to 80% of the model window, leaving
// headroom for the reply and estTokens error. clamped is true only when an
// explicit config value got cut — surfaced as a warning in status and
// /context; the silent default just fits whatever model is selected.
func chatBudget(provider string) (budget int, clamped bool) {
	limit := int(modelWindow(chatModel(provider)) * 4 / 5)
	b := readConfig().TokenBudget
	explicit := b > 0
	if b == 0 {
		b = defaultTokenBudget
	}
	if b > limit {
		return limit, explicit
	}
	return b, false
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

// chatProvider resolves which provider a session's chat call hits: its
// sticky pin, else the default llm ("claude" when nothing is connected —
// the call fails before the budget matters then).
func (s *Server) chatProvider(sess Session) string {
	if sess.Provider != "" {
		return sess.Provider
	}
	for _, ls := range s.llmStatuses() {
		if ls.Default {
			return ls.Name
		}
	}
	return "claude"
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
	go s.refreshPin()
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
	return s.chatAnswer(ctx, sess, text)
}

// chatAnswer is the session-scoped core: background workers pass the session
// they were enqueued for, so a queued message never lands in whatever session
// became active meanwhile.
func (s *Server) chatAnswer(ctx context.Context, sess Session, text string) (string, error) {
	// a new message supersedes any pending proposal in this session (the ✏
	// edit flow is just this: the button only tells the owner to type)
	if n, err := s.store.DeleteSessionProposals(sess.ID); err == nil && n > 0 {
		s.store.AddMessage(sess.ID, "user",
			"🚫 pending proposal cancelled — those actions were NOT run", time.Now().Unix())
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
	persona := readPersona() + "\n\n" + cronPrompt(s.store) + "\n\n" + filePrompt() + "\n\n" + taskPrompt(s.store) +
		"\n\n" + plansPrompt() + "\n\n" + corePrompt(wiki, sess)
	if sess.Plan {
		persona += "\n\n" + planContract()
	}
	budget, _ := chatBudget(s.chatProvider(sess))
	prompt, dropped := composePrompt(persona, memory, history, text, budget)
	reply, err := s.answerWith(ctx, sess.Provider, prompt)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reply) == "" {
		reply = "(empty reply)" // telegram rejects empty text; history stays truthful
	}
	// "" from here on means "proposal delivered out-of-band, send nothing"
	if len(gatedNames(reply)) > 0 {
		return "", s.proposeTools(ctx, sess, reply)
	}
	// a planning question with tap options: park the raw turn in history and
	// push the keyboard out-of-band, like a proposal (gate wins when both
	// appear — the check above ran first)
	if q, opts, ok := parseAsk(reply); ok {
		if _, err := s.store.AddMessage(sess.ID, "assistant", reply, time.Now().Unix()); err != nil {
			return "", err
		}
		var kb [][]button
		for _, o := range opts {
			kb = append(kb, []button{{Text: o, CallbackData: "opt:" + truncBytes(o, 60)}})
		}
		s.notifyOwner(ctx, tgReply{Text: askVisible(reply, q), Keyboard: kb})
		return "", nil
	}
	planSaved := sess.Plan && strings.Contains(reply, "TOOL:plan_save") // approvals-off path
	readRan := strings.Contains(reply, "TOOL:read_file")
	reply = s.applyTools(ctx, sess.ID, reply) // history keeps confirmations, not TOOL lines
	if planSaved {
		s.store.SetSessionPlan(sess.ID, false) // best-effort; interview over
	}
	visible := reply
	if readRan {
		// one follow-up round so read_file content is used this turn, not
		// next; the user sees only the final answer, history keeps both. A
		// read_file in the follow-up itself still lands a turn late.
		followup := prompt + "\n\nassistant: " + reply +
			"\n\nThe 📄 lines above are the file contents you asked for. Answer the user's message now using them; do not emit TOOL:read_file again."
		if more, err := s.answerWith(ctx, sess.Provider, followup); err == nil && strings.TrimSpace(more) != "" {
			if len(gatedNames(more)) > 0 {
				// round 1 ran (📄 dumps); persist it, then gate round 2 — file
				// contents are untrusted input, this round must not skip the gate
				s.store.AddMessage(sess.ID, "assistant", reply, time.Now().Unix())
				return "", s.proposeTools(ctx, sess, more)
			}
			visible = s.applyTools(ctx, sess.ID, more)
			reply += "\n\n" + visible
		}
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
	return visible, nil
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
		s.refreshPin()                    // dashboard headline shows the new name
	}
}
