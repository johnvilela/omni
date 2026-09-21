package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

const memoryPage = "omni/long-term.md"

// stripFrontmatter returns the body after a leading ---\n...\n---\n block.
func stripFrontmatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	rest := s[4:]
	i := strings.Index(rest, "\n---\n")
	if i < 0 {
		return s
	}
	return strings.TrimLeft(rest[i+5:], "\n")
}

func (s *Server) readMemory(ctx context.Context) string {
	if s.aiMemory == nil {
		return ""
	}
	body, err := s.aiMemory.readPage(ctx, memoryPage)
	if err != nil {
		return ""
	}
	return body
}

func (s *Server) writeMemory(ctx context.Context, body string) error {
	if s.aiMemory == nil {
		return fmt.Errorf("ai-memory is not running")
	}
	return s.aiMemory.writePage(ctx, memoryPage, body, []string{"omni", "long-term"}, false)
}

// Core memory: owner-approved facts saved via /memory, one page per theme
// under core/. Highest priority and theme-scoped: every prompt carries the
// theme index plus the "general" page whole; other themes are loaded into a
// session on demand (memory_load) and stick. onCompaction never touches
// core/ — /memory facts are never condensed away.
const coreDir = "omni/core"

// corePath is the page path for one theme slug.
func corePath(theme string) string {
	return coreDir + "/" + theme + ".md"
}

// coreTheme normalizes a model- or owner-supplied theme name to a page slug;
// empty in, empty out (callers fall back to "general").
func coreTheme(name string) string {
	return strings.Trim(strings.ToLower(sanitizeName(name)), "-")
}

// coreThemes lists the saved theme slugs, lexicographic.
func (s *Server) coreThemes(ctx context.Context) []string {
	if s.aiMemory == nil {
		return nil
	}
	var ts []string
	for _, path := range s.aiMemory.searchPaths(ctx, "Omni core memory theme", coreDir+"/") {
		name := strings.TrimPrefix(path, coreDir+"/")
		if t, ok := strings.CutSuffix(name, ".md"); ok {
			ts = append(ts, t)
		}
	}
	sort.Strings(ts)
	return ts
}

// countFacts counts the "- " bullets of a theme page body.
func countFacts(facts string) int {
	n := strings.Count(facts, "\n- ")
	if strings.HasPrefix(facts, "- ") {
		n++
	}
	return n
}

// readCoreTheme returns one theme page's facts, "" when absent.
func (s *Server) readCoreTheme(ctx context.Context, theme string) string {
	if s.aiMemory == nil {
		return ""
	}
	body, err := s.aiMemory.readPage(ctx, corePath(theme))
	if err != nil {
		return ""
	}
	return body
}

// appendCore adds one fact bullet to a theme page, creating it on first use.
func (s *Server) appendCore(ctx context.Context, theme, fact string) error {
	body := s.readCoreTheme(ctx, theme)
	if body == "" {
		body = "# Omni core memory theme: " + theme + "\n"
	}
	body = strings.TrimRight(body, "\n") + "\n\n- " + fact + "\n"
	return s.aiMemory.writePage(ctx, corePath(theme), body, []string{"omni", "core", theme}, true)
}

// corePrompt is the core-memory section injected into every chat prompt: the
// theme index, the always-on "general" facts plus the session's loaded
// themes, and the load/save contracts.
func (s *Server) corePrompt(ctx context.Context, sess Session) string {
	var b strings.Builder
	b.WriteString("## Core memory\n\nOwner-approved durable facts — HIGHEST priority, they override anything\nelse you remember. Themes:\n")
	themes := s.coreThemes(ctx)
	if len(themes) == 0 {
		b.WriteString("none yet\n")
	}
	loaded := append([]string{"general"}, strings.Split(sess.Themes, ",")...)
	for _, t := range themes {
		facts := s.readCoreTheme(ctx, t)
		if slices.Contains(loaded, t) && facts != "" {
			fmt.Fprintf(&b, "- %s:\n%s", t, facts)
		} else {
			fmt.Fprintf(&b, "- %s (%d facts, not loaded)\n", t, countFacts(facts))
		}
	}
	b.WriteString(`
When the chat topic matches an unloaded theme, load it with this line alone
on its own line (it sticks to this session):
TOOL:memory_load {"theme":"..."}
When the owner explicitly asks you to remember something permanently, reply
with this line alone (one short sentence, their language; reuse a theme when
one fits, else invent a short one; "general" = always visible):
TOOL:memory_save {"theme":"...","text":"..."}
Saving happens only after owner approval.`)
	return b.String()
}

// handleMemory is the /memory command: condense the text into one fact via a
// background utility llm call, park it as a proposal on the active session
// (approve/deny/edit buttons for free), append to its theme page on approval.
// An optional single-word "theme:" prefix forces the theme.
func (s *Server) handleMemory(arg string) tgReply {
	if arg == "" {
		return tgReply{Text: "usage: /memory [theme:] <text> — save one durable fact to core memory"}
	}
	if s.aiMemory == nil {
		return tgReply{Text: "⚠ ai-memory is not running — rerun scripts/install.sh"}
	}
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	theme, text := "", arg
	if head, rest, ok := strings.Cut(arg, ":"); ok && !strings.Contains(head, " ") && strings.TrimSpace(rest) != "" {
		theme, text = coreTheme(head), strings.TrimSpace(rest)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		prompt := "Condense this into ONE durable fact about the owner: one short sentence, their language, no preamble or quotes."
		if theme == "" {
			themes := strings.Join(s.coreThemes(ctx), ", ")
			if themes == "" {
				themes = "none yet"
			}
			prompt += " Also pick a theme (existing themes: " + themes + " — reuse one when it fits, else invent a short lowercase one; \"general\" = always relevant). Reply exactly: <theme>|<fact>"
		} else {
			prompt += " Reply with the fact only."
		}
		out, err := s.Answer(withAICall(ctx, sess.ID, "memory.condense"), prompt+"\n\n"+text)
		if err != nil || strings.TrimSpace(out) == "" {
			msg := "empty reply"
			if err != nil {
				msg = err.Error()
			}
			s.notifyOwner(ctx, tgReply{Text: "⚠ memory: " + msg})
			return
		}
		fact := strings.TrimSpace(out)
		if theme == "" {
			if t, f, ok := strings.Cut(fact, "|"); ok {
				theme, fact = coreTheme(t), strings.TrimSpace(f)
			}
		}
		if theme == "" {
			theme = "general"
		}
		args, _ := json.Marshal(struct {
			Theme string `json:"theme"`
			Text  string `json:"text"`
		}{theme, fact})
		raw := "TOOL:memory_save " + string(args)
		if len(gatedNames(raw)) == 0 { // approvals off: save directly
			s.notifyOwner(ctx, tgReply{Text: s.applyTools(ctx, sess.ID, raw)})
			return
		}
		if err := s.proposeTools(ctx, sess, raw); err != nil {
			s.notifyOwner(ctx, tgReply{Text: "⚠ memory: " + err.Error()})
		}
	}()
	return tgReply{Text: "🧠 condensing…"}
}

// onCompaction folds turns that fell out of the context budget into the
// long-term memory page — the moment they'd otherwise be forgotten. Sole
// compaction point — becomes a hook event when omni grows hooks; callers hold
// s.digesting.
func (s *Server) onCompaction(sessionID string, overflow []Message) {
	defer s.digesting.Store(false)
	if s.aiMemory == nil {
		return
	}
	var b strings.Builder
	b.WriteString("You maintain the assistant's long-term memory page about its owner.\n\nCurrent page:\n")
	if cur := s.readMemory(context.Background()); cur != "" {
		b.WriteString(cur)
	} else {
		b.WriteString("(empty)")
	}
	b.WriteString("\n\nConversation turns about to be forgotten:\n")
	for _, m := range overflow {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	b.WriteString("\nRewrite the complete page: merge durable facts about the owner and ongoing topics; drop chit-chat; keep it under about 300 words. If nothing durable is new, return the current page unchanged. Reply with the page body only.")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	body, err := s.Answer(withAICall(ctx, sessionID, "memory.consolidate"), b.String())
	if err != nil || body == "" {
		return // never clobber the page with nothing; retries on the next overflow
	}
	if err := s.writeMemory(ctx, body); err != nil {
		return
	}
	// ponytail: a crash between the write above and this bump re-merges the
	// same turns next time; the "return unchanged if nothing new" prompt
	// makes that harmless.
	s.store.SetConsolidatedUntil(sessionID, overflow[len(overflow)-1].ID)
}
