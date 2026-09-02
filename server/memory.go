package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Long-term memory lives in memoria's global wiki as one page the bot reads
// into every prompt and rewrites at compaction time. omni talks to it with
// plain file IO: memoria has no CLI write command, its page format is
// trivial, and pages outside sessions/ never decay.
const memoryPage = "omni-bot/memory.md"

// memoriaWiki returns memoria's global wiki dir, or "" when memoria isn't
// set up (`memoria bootstrap --global` not run) — every memory feature then
// silently no-ops. Root: global_path from memoria's config.yaml, else the
// memoria config dir itself.
func memoriaWiki() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	root := filepath.Join(dir, "memoria")
	var cfg struct {
		GlobalPath string `yaml:"global_path"`
	}
	if data, err := os.ReadFile(filepath.Join(root, "config.yaml")); err == nil {
		if yaml.Unmarshal(data, &cfg) == nil && cfg.GlobalPath != "" {
			root = cfg.GlobalPath
		}
	}
	wiki := filepath.Join(root, "wiki")
	if fi, err := os.Stat(wiki); err != nil || !fi.IsDir() {
		return ""
	}
	return wiki
}

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

// readMemory returns the memory page body, "" when absent.
func readMemory(wiki string) string {
	data, err := os.ReadFile(filepath.Join(wiki, memoryPage))
	if err != nil {
		return ""
	}
	return stripFrontmatter(string(data))
}

// writeMemory writes the page in memoria's exact on-disk format (frontmatter
// is memoria-owned: tags only).
func writeMemory(wiki, body string) error {
	path := filepath.Join(wiki, memoryPage)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("---\ntags: [omni-bot]\n---\n\n"+body), 0o644)
}

// onCompaction folds turns that fell out of the context budget into the
// long-term memory page — the moment they'd otherwise be forgotten. Sole
// compaction point — becomes a hook event when omni grows hooks; callers hold
// s.digesting.
func (s *Server) onCompaction(sessionID string, overflow []Message) {
	defer s.digesting.Store(false)
	wiki := memoriaWiki()
	if wiki == "" {
		return
	}
	var b strings.Builder
	b.WriteString("You maintain the assistant's long-term memory page about its owner.\n\nCurrent page:\n")
	if cur := readMemory(wiki); cur != "" {
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
	body, err := s.Answer(ctx, b.String())
	if err != nil || body == "" {
		return // never clobber the page with nothing; retries on the next overflow
	}
	if err := writeMemory(wiki, body); err != nil {
		return
	}
	// ponytail: a crash between the write above and this bump re-merges the
	// same turns next time; the "return unchanged if nothing new" prompt
	// makes that harmless.
	s.store.SetConsolidatedUntil(sessionID, overflow[len(overflow)-1].ID)
}
