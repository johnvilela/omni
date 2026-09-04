// Installed plugins: the CLI (cli/plugins.go) downloads the binary and writes
// a manifest snapshot per plugin into <data>/plugins/; the server only reads
// those to dispatch plugin telegram commands and publish them in the menu.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// pluginTimeout caps one plugin command exec.
const pluginTimeout = 60 * time.Second

// pluginManifest mirrors cli/plugins.go (project pattern: no shared package).
type pluginMCP struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

type pluginCommand struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Argv        []string `json:"argv,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
}

type pluginManifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	MCP         *pluginMCP      `json:"mcp,omitempty"`
	Skills      bool            `json:"skills,omitempty"`
	Commands    []pluginCommand `json:"commands,omitempty"`
	Repo        string          `json:"repo,omitempty"`
	SkillDirs   []string        `json:"skill_dirs,omitempty"`
}

func pluginsDir() string { return filepath.Join(dataDir(), "plugins") }

// loadPluginManifests reads every snapshot, skipping unparsable ones; Glob
// returns sorted paths, so command order is stable.
func loadPluginManifests() []pluginManifest {
	files, _ := filepath.Glob(filepath.Join(pluginsDir(), "*.json"))
	var ms []pluginManifest
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var m pluginManifest
		if json.Unmarshal(data, &m) != nil || m.Name == "" {
			continue
		}
		ms = append(ms, m)
	}
	return ms
}

// resolveBin turns a bare command name into its ~/.local/bin path — the
// server's systemd PATH does not include it.
func resolveBin(cmd string) string {
	if strings.ContainsRune(cmd, os.PathSeparator) {
		return cmd
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return cmd
	}
	p := filepath.Join(home, ".local", "bin", cmd)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return cmd
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// codexMCPArgs renders installed plugin MCP servers as codex -c overrides —
// codex has no per-project mcp config file, only dotted-path TOML values.
func codexMCPArgs() []string {
	var args []string
	for _, m := range loadPluginManifests() {
		if m.MCP == nil {
			continue
		}
		args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.command=%q", m.Name, resolveBin(m.MCP.Command)))
		if len(m.MCP.Args) > 0 {
			j, _ := json.Marshal(m.MCP.Args)
			args = append(args, "-c", fmt.Sprintf("mcp_servers.%s.args=%s", m.Name, j))
		}
	}
	return args
}

// pluginTgCommands renders every installed plugin command for setMyCommands.
func pluginTgCommands() []map[string]string {
	var out []map[string]string
	for _, m := range loadPluginManifests() {
		for _, c := range m.Commands {
			out = append(out, map[string]string{"command": c.Name, "description": c.Description})
		}
	}
	return out
}

// pluginReply dispatches one slash command to the plugin that declared it:
// exec the declared argv plus the user's args, relay stdout. ok=false means
// no plugin claims the command and handleMessage falls through.
// ponytail: the exec blocks the poll loop for up to pluginTimeout; route
// through the session queue if a plugin ever gets slow enough to hurt.
func (s *Server) pluginReply(ctx context.Context, cmd, arg string) (tgReply, bool) {
	name := strings.ReplaceAll(strings.TrimPrefix(cmd, "/"), "-", "_")
	for _, m := range loadPluginManifests() {
		for _, c := range m.Commands {
			if c.Name != name {
				continue
			}
			if c.Prompt != "" {
				return s.pluginAgentReply(ctx, c, arg), true
			}
			if len(c.Argv) == 0 {
				continue
			}
			argv := append(slices.Clone(c.Argv), strings.Fields(arg)...)
			out, err := runCLI(ctx, "", pluginTimeout, resolveBin(argv[0]), argv[1:]...)
			if err != nil {
				return tgReply{Text: "⚠ " + err.Error()}, true
			}
			if strings.TrimSpace(out) == "" {
				return tgReply{Text: "✓ (no output)"}, true
			}
			return tgReply{Text: strings.TrimSpace(out)}, true
		}
	}
	return tgReply{}, false
}

// pluginAgentReply runs a prompt-declared command as a fresh agent session —
// the /agent shape, so the reply arrives async via the queue instead of
// blocking the poll loop (an LLM turn would blow past pluginTimeout).
func (s *Server) pluginAgentReply(ctx context.Context, c pluginCommand, arg string) tgReply {
	provider, note := agentProvider()
	sess, err := s.newSession(true, provider)
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if err := ensureAgentDir(); err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	s.clearChats(ctx) // before enqueue: the task's answer must outlive the clear
	s.enqueue(sess.ID, pluginAgentText(c, arg, s.store))
	return tgReply{Text: note + "⏳ /" + c.Name + " running (" + provider + ")", DeleteInbound: true}
}

// pluginAgentText composes the session's first message: the declared prompt,
// the owner's raw trailing words (not word-split — punctuation matters to a
// prompt), then the omni context the session can act on: where plan pages
// live and the scheduled-jobs contract.
func pluginAgentText(c pluginCommand, arg string, store *Store) string {
	var b strings.Builder
	b.WriteString(c.Prompt)
	if arg != "" {
		b.WriteString("\n\nOwner's message: " + arg)
	}
	if wiki := memoriaWiki(); wiki != "" {
		fmt.Fprintf(&b, "\n\nPlan pages live at %s — markdown with `status: active|done` frontmatter; edit them with your file tools.",
			filepath.Join(wiki, plansDir, "<slug>.md"))
	}
	b.WriteString("\n\n" + cronPrompt(store))
	return b.String()
}

// syncPlugins re-publishes the telegram command menu (built-ins + plugins);
// best-effort — no live poller just means the next connect publishes it.
func (s *Server) syncPlugins(ctx context.Context) (int, bool) {
	extra := pluginTgCommands()
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return len(extra), false
	}
	if err := tg.registerCommands(ctx, extra); err != nil {
		log.Printf("plugins: setMyCommands: %v", err)
		return len(extra), false
	}
	return len(extra), true
}
