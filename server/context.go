package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ctxWindow is the model context window /context measures agent sessions
// against; chat sessions use the compose token budget instead.
var ctxWindow = map[string]int64{"claude": 200_000, "openai": 272_000}

// showContext renders /context for the active session: a usage bar plus a
// per-source token breakdown.
func (s *Server) showContext() tgReply {
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	history, err := s.store.Messages(sess.ID)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if sess.Agent {
		return tgReply{Text: agentContext(sess, history)}
	}
	persona := readPersona() + "\n\n" + cronPrompt(s.store)
	var memory string
	if wiki := memoriaWiki(); wiki != "" {
		memory = readMemory(wiki)
	}
	provider := s.chatProvider(sess)
	budget, clamped := chatBudget(provider)
	text := chatContext(persona, memory, history, budget)
	if clamped {
		text += fmt.Sprintf("\n⚠ token_budget %s exceeds %s's window — clamped",
			fmtTok(int64(readConfig().TokenBudget)), chatModel(provider))
	}
	return tgReply{Text: text}
}

// chatContext mirrors composePrompt's budget math: persona and memory always
// enter whole, then history newest→oldest until the budget is spent.
func chatContext(persona, memory string, history []Message, budget int) string {
	pTok, mTok := estTokens(persona), estTokens(memory)
	remaining := budget - pTok - mTok
	keep := len(history)
	for keep > 0 && estTokens(history[keep-1].Content) <= remaining {
		remaining -= estTokens(history[keep-1].Content)
		keep--
	}
	var hTok int
	for _, m := range history[keep:] {
		hTok += estTokens(m.Content)
	}
	used := pTok + mTok + hTok
	pct := float64(used) / float64(budget) * 100

	var b strings.Builder
	fmt.Fprintf(&b, "💬 chat context — budget %s\n", fmtTok(int64(budget)))
	fmt.Fprintf(&b, "%s %.0f%% (%s used)\n", bar(pct), pct, fmtTok(int64(used)))
	fmt.Fprintf(&b, "persona (AGENTS.md): %s\n", fmtTok(int64(pTok)))
	if mTok > 0 {
		fmt.Fprintf(&b, "memory: %s\n", fmtTok(int64(mTok)))
	}
	fmt.Fprintf(&b, "messages: %s", fmtTok(int64(hTok)))
	if keep > 0 {
		fmt.Fprintf(&b, " (%d/%d fit)", len(history)-keep, len(history))
	}
	fmt.Fprintf(&b, "\nfree: %s", fmtTok(max(0, int64(budget-used))))
	return b.String()
}

// agentContext measures an agent session against the model window. The total
// is the CLI's real reported context size from the last turn; per-source
// numbers are bytes/4 estimates from the files the CLI loads. System prompt
// and tool schemas (MCP included) can't be read from disk, so they show as
// the remainder of the real total — unknown before the first turn.
func agentContext(sess Session, history []Message) string {
	provider := sess.Provider
	if provider == "" {
		provider = "claude"
	}
	window := ctxWindow[provider]
	if window == 0 {
		window = 200_000
	}
	aTok := int64(agentsMDTokens(provider))
	var sTok int64
	if provider != "openai" { // codex has no skills mechanism
		sTok = int64(skillTokens())
	}
	var hTok int64
	for _, m := range history {
		hTok += int64(estTokens(m.Content))
	}
	used, rest := sess.LastCtx, sess.LastCtx-aTok-sTok-hTok
	if used == 0 {
		used, rest = aTok+sTok+hTok, -1 // no turn yet: estimates only
	} else if rest < 0 {
		rest = 0
	}
	pct := float64(used) / float64(window) * 100

	var b strings.Builder
	fmt.Fprintf(&b, "🤖 agent context (%s) — window %s\n", provider, fmtTok(window))
	fmt.Fprintf(&b, "%s %.0f%% (%s used)\n", bar(pct), pct, fmtTok(used))
	fmt.Fprintf(&b, "AGENTS.md files: ~%s\n", fmtTok(aTok))
	if provider != "openai" {
		fmt.Fprintf(&b, "skills: ~%s\n", fmtTok(sTok))
	}
	fmt.Fprintf(&b, "messages: ~%s\n", fmtTok(hTok))
	if rest >= 0 {
		fmt.Fprintf(&b, "system & MCP tools: ~%s\n", fmtTok(rest))
	} else {
		b.WriteString("system & MCP tools: ? (measured after the first turn)\n")
	}
	fmt.Fprintf(&b, "free: %s", fmtTok(max(0, window-used)))
	return b.String()
}

// agentsMDTokens estimates the instruction files the provider auto-loads:
// the workspace seed plus the user-global one.
func agentsMDTokens(provider string) int {
	home, _ := os.UserHomeDir()
	paths := []string{filepath.Join(agentDir(), "CLAUDE.md"), filepath.Join(home, ".claude", "CLAUDE.md")}
	if provider == "openai" {
		paths = []string{filepath.Join(agentDir(), "AGENTS.md"), filepath.Join(home, ".codex", "AGENTS.md")}
	}
	var tok int
	for _, p := range paths {
		if raw, err := os.ReadFile(p); err == nil {
			tok += estTokens(string(raw))
		}
	}
	return tok
}

// skillTokens estimates claude's skill listing: only each SKILL.md's yaml
// frontmatter (name + description) enters context, not the body.
func skillTokens() int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "skills", "*", "SKILL.md"))
	var tok int
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		fm := string(raw)
		if rest, ok := strings.CutPrefix(fm, "---\n"); ok {
			if head, _, found := strings.Cut(rest, "\n---"); found {
				fm = head
			}
		}
		tok += estTokens(fm)
	}
	return tok
}
