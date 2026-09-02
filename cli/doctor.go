package main

// omni doctor: one-shot health report. Everything runs CLI-side so the
// command stays useful when the server itself is the problem; the guardian's
// periodic runtime checks (disk, memory, sqlite, creds) are not re-run here —
// their verdict is read from guardian.json instead.

import (
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type check struct {
	name string
	ok   bool
	skip bool   // informational: rendered dim, never counts as a failure
	fix  string // command printed under a failing check
	info string // extra dim lines under the check (multi-line allowed)
}

func failCount(cs []check) int {
	n := 0
	for _, c := range cs {
		if !c.ok && !c.skip {
			n++
		}
	}
	return n
}

func renderSection(title string, cs []check) string {
	s := "\n" + titleStyle.Render("  "+title) + "\n\n"
	for _, c := range cs {
		switch {
		case c.skip:
			s += "  " + dimStyle.Render("– "+c.name) + "\n"
		case c.ok:
			s += "  " + okStyle.Render("✓") + " " + c.name + "\n"
		default:
			s += "  " + errStyle.Render("✗") + " " + c.name + "\n"
		}
		if c.info != "" {
			for _, line := range strings.Split(strings.TrimSpace(c.info), "\n") {
				s += dimStyle.Render("      "+line) + "\n"
			}
		}
		if !c.ok && !c.skip && c.fix != "" {
			s += "      " + dimStyle.Render("fix: ") + cmdStyle.Render(c.fix) + "\n"
		}
	}
	return s
}

// installScript is the rebuild/reinstall fix for whichever flavor this is.
func installScript() string {
	if strings.HasSuffix(app, "-dev") {
		return "scripts/dev.sh"
	}
	return "scripts/install.sh"
}

func have(names ...string) bool {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}

func installChecks() []check {
	var cs []check

	bins := []string{app, app + "-server", app + "-guardian"}
	var missing, offPath []string
	home, _ := os.UserHomeDir()
	for _, b := range bins {
		switch {
		case have(b):
		default:
			if _, err := os.Stat(filepath.Join(home, ".local", "bin", b)); err == nil {
				offPath = append(offPath, b)
			} else {
				missing = append(missing, b)
			}
		}
	}
	if len(missing) == 0 && len(offPath) == 0 {
		cs = append(cs, check{name: strings.Join(bins, ", ") + " installed", ok: true})
	}
	if len(missing) > 0 {
		cs = append(cs, check{name: strings.Join(missing, ", ") + " not installed", fix: installScript()})
	}
	if len(offPath) > 0 {
		cs = append(cs, check{name: strings.Join(offPath, ", ") + " in ~/.local/bin but not on PATH", fix: "add ~/.local/bin to your PATH"})
	}

	// agent stack — installed by install.sh's dependency block on any flavor
	var stackMissing []string
	for _, b := range []string{"node", "chromium", "playwright-cli", "memoria"} {
		found := have(b)
		if b == "chromium" {
			found = have("chromium", "chromium-browser")
		}
		if !found {
			stackMissing = append(stackMissing, b)
		}
	}
	if len(stackMissing) == 0 {
		cs = append(cs, check{name: "agent stack: node, chromium, playwright-cli, memoria", ok: true})
	} else {
		cs = append(cs, check{name: "agent stack missing: " + strings.Join(stackMissing, ", "), fix: "scripts/install.sh"})
	}

	var vendorMissing []string
	for _, b := range []string{"claude", "codex", "gemini"} {
		if !have(b) {
			vendorMissing = append(vendorMissing, b)
		}
	}
	if len(vendorMissing) == 0 {
		cs = append(cs, check{name: "vendor clis: claude, codex, gemini", ok: true})
	} else {
		cs = append(cs, check{name: strings.Join(vendorMissing, ", ") + " cli not installed (optional — api keys work without them)", skip: true})
	}

	if _, err := os.Stat(filepath.Join(dataDir(), "omni.db")); err == nil {
		cs = append(cs, check{name: "database " + filepath.Join(dataDir(), "omni.db"), ok: true})
	} else {
		cs = append(cs, check{name: "database missing (the server creates it at first start)", fix: "systemctl --user start " + app + "-server"})
	}

	if _, err := os.Stat(filepath.Join(dataDir(), "agent", "chrome-profile")); err == nil {
		cs = append(cs, check{name: "agent workspace with chrome profile", ok: true})
	} else {
		cs = append(cs, check{name: "agent workspace " + filepath.Join(dataDir(), "agent", "chrome-profile") + " missing", fix: "scripts/install.sh"})
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(configPath()), "AGENTS.md")); err == nil {
		cs = append(cs, check{name: "persona ~/.config/" + app + "/AGENTS.md", ok: true})
	} else {
		cs = append(cs, check{name: "persona AGENTS.md missing (the server seeds it at start)", fix: "systemctl --user restart " + app + "-server"})
	}

	return cs
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, app, "config.yaml")
}

// doctorConfig mirrors the server's Config so a value of the wrong type
// (e.g. token_budget: "abc") fails the same way it fails the server.
type doctorConfig struct {
	TelegramToken string `yaml:"telegram_token"`
	OpenAIKey     string `yaml:"openai_key"`
	AnthropicKey  string `yaml:"anthropic_key"`
	GeminiKey     string `yaml:"gemini_key"`
	DefaultLLM    string `yaml:"default_llm"`
	OpenAIModel   string `yaml:"openai_model"`
	ClaudeModel   string `yaml:"claude_model"`
	GeminiModel   string `yaml:"gemini_model"`
	OpenAIEffort  string `yaml:"openai_effort"`
	ClaudeEffort  string `yaml:"claude_effort"`
	GeminiEffort  string `yaml:"gemini_effort"`
	TokenBudget   int    `yaml:"token_budget"`
}

func configChecks() []check {
	display := "~/.config/" + app + "/config.yaml"
	data, err := os.ReadFile(configPath())
	if err != nil {
		return []check{{name: "no config.yaml yet (written when you first connect a channel or save a key)", skip: true}}
	}
	var cfg doctorConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return []check{{
			name: display + " is broken — the server silently ignores the WHOLE file, telegram token included",
			fix:  "edit " + configPath(),
			info: err.Error(),
		}}
	}
	cs := []check{{name: display + " parses", ok: true}}
	if cfg.DefaultLLM != "" && !slices.Contains(llmProviderNames, cfg.DefaultLLM) {
		cs = append(cs, check{
			name: fmt.Sprintf("default_llm %q is not one of %s", cfg.DefaultLLM, strings.Join(llmProviderNames, ", ")),
			fix:  "omni llm set-default",
		})
	}
	return cs
}

// unitChecks reports one systemd user unit: installed → enabled → active.
func unitChecks(unit, enableFix, activeFix string) []check {
	switch state, _ := systemctlUser("is-enabled", unit); state {
	case "enabled":
	case "disabled":
		return []check{{name: unit + " disabled", fix: enableFix}}
	default:
		return []check{{name: unit + " not installed", fix: installScript()}}
	}
	if active, _ := systemctlUser("is-active", unit); active != "active" {
		return []check{{name: unit + " enabled but " + active, fix: activeFix}}
	}
	return []check{{name: unit + " enabled and active", ok: true}}
}

func serviceChecks() []check {
	cs := unitChecks(app+"-server.service",
		"systemctl --user enable --now "+app+"-server.service",
		"systemctl --user restart "+app+"-server")
	cs = append(cs, unitChecks(guardianTimer(),
		"omni guardian --enabled=true",
		"omni guardian --enabled=true")...)

	alerts := guardianAlerts()
	if len(alerts) == 0 {
		cs = append(cs, check{name: "no active guardian alerts", ok: true})
		return cs
	}
	for _, name := range slices.Sorted(maps.Keys(alerts)) {
		cs = append(cs, check{
			name: "guardian alert: " + name + " — red since " + alerts[name],
			fix:  "journalctl --user -u " + app + "-guardian -n 20",
		})
	}
	return cs
}

// llmCheck is the default-vs-connected rule in one pure function.
func llmCheck(ls []LLM) check {
	connected := 0
	var def *LLM
	for i, l := range ls {
		if l.Connected {
			connected++
		}
		if l.Default {
			def = &ls[i]
		}
	}
	switch {
	case def != nil && def.Connected:
		return check{name: "llm: " + def.Name + " is the default and connected", ok: true}
	case def != nil:
		return check{name: "llm: default " + def.Name + " is disconnected — the bot cannot answer", fix: "omni llm connect -p " + def.Name}
	case connected == 0:
		return check{name: "llm: no provider connected — the bot cannot answer", fix: "omni llm connect"}
	default:
		return check{name: "llm: no default among the connected providers", fix: "omni llm set-default"}
	}
}

func serverChecks(c *Client) []check {
	st, err := c.Status()
	if err != nil {
		return []check{
			{name: "server not responding at " + c.Base, fix: "systemctl --user restart " + app + "-server"},
			{name: "version, telegram, llm and pairing checks skipped (server down)", skip: true},
		}
	}
	cs := []check{{name: "server up at " + c.Base, ok: true}}
	if st.App != app {
		cs = append(cs, check{
			name: fmt.Sprintf("a different app (%s) answers at %s — dev/prod mixup?", st.App, c.Base),
			fix:  "unset OMNI_ADDR (or point it at the right port)",
		})
	}
	if st.Version == version {
		cs = append(cs, check{name: "cli and server both " + version, ok: true})
	} else {
		cs = append(cs, check{name: "version mismatch: cli " + version + ", server " + st.Version, fix: installScript()})
	}

	telegramUp := false
	if chs, err := c.Channels(); err != nil {
		cs = append(cs, check{name: "channels: " + err.Error()})
	} else {
		for _, ch := range chs {
			if ch.Name == "telegram" && ch.Connected {
				telegramUp = true
			}
		}
		if telegramUp {
			cs = append(cs, check{name: "telegram connected", ok: true})
		} else {
			cs = append(cs, check{name: "telegram not connected", fix: "omni channels connect -c telegram"})
		}
	}

	if ls, err := c.LLMs(); err != nil {
		cs = append(cs, check{name: "llm: " + err.Error()})
	} else {
		cs = append(cs, llmCheck(ls))
		for _, l := range ls {
			if l.BudgetNote != "" {
				cs = append(cs, check{name: l.Name + ": " + l.BudgetNote, skip: true})
			}
		}
	}

	switch {
	case !telegramUp:
		cs = append(cs, check{name: "pairing check skipped (telegram not connected)", skip: true})
	default:
		ps, err := c.Pairings("telegram")
		if err != nil {
			cs = append(cs, check{name: "pairing: " + err.Error()})
			break
		}
		approved := 0
		for _, p := range ps {
			if p.Approved {
				approved++
			}
		}
		if approved > 0 {
			cs = append(cs, check{name: fmt.Sprintf("%d approved pairing(s)", approved), ok: true})
		} else {
			cs = append(cs, check{
				name: "no approved pairing — replies, cron and guardian alerts have no recipient",
				fix:  "message the bot, then: omni pairing approve telegram <code>",
			})
		}
	}
	return cs
}

// ponytail: naive keyword filter — the services log without levels, so the
// journal has no error priority to query; upgrade to structured levels in the
// server if this ever misses real errors.
var errLine = regexp.MustCompile(`(?i)\b(error|failed|panic|fatal|timeout)\b|⚠`)

// telegram API URLs in log lines embed the bot token; doctor output gets
// pasted into bug reports, so redact it
var botToken = regexp.MustCompile(`bot\d+:[\w-]+`)

func journalChecks() []check {
	var cs []check
	for _, unit := range []string{app + "-server", app + "-guardian"} {
		out, err := exec.Command("journalctl", "--user", "-u", unit,
			"--since", "-24h", "-n", "500", "--no-pager", "-o", "cat", "-q").Output()
		if err != nil {
			cs = append(cs, check{name: unit + " journal unavailable", skip: true})
			continue
		}
		var hits []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if errLine.MatchString(line) {
				hits = append(hits, botToken.ReplaceAllString(line, "bot***"))
			}
		}
		if len(hits) == 0 {
			cs = append(cs, check{name: unit + ": no errors in the last 24h", ok: true})
			continue
		}
		shown := hits
		if len(shown) > 10 {
			shown = shown[len(shown)-10:]
		}
		cs = append(cs, check{
			name: fmt.Sprintf("%s: %d error line(s) in the last 24h", unit, len(hits)),
			info: strings.Join(shown, "\n"),
			fix:  "journalctl --user -u " + unit + " -e",
		})
	}
	return cs
}

func runDoctor() int {
	// own client: the stock 30s timeout would stall the report on a
	// firewalled-but-not-refused port
	c := &Client{Base: serverURL(), http: &http.Client{Timeout: 5 * time.Second}}
	sections := []struct {
		title string
		cs    []check
	}{
		{"INSTALL", installChecks()},
		{"CONFIG", configChecks()},
		{"SERVICES", serviceChecks()},
		{"SERVER", serverChecks(c)},
		{"RECENT ERRORS", journalChecks()},
	}
	fails := 0
	for _, s := range sections {
		fmt.Print(renderSection(s.title, s.cs))
		fails += failCount(s.cs)
	}
	if fails > 0 {
		fmt.Println("\n  " + errStyle.Render(fmt.Sprintf("%d check(s) failed", fails)))
		return 1
	}
	fmt.Println("\n  " + okStyle.Render("✓ all checks passed"))
	return 0
}
