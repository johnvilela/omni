package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	omniversion "omni/version"
)

// app and defaultAddr are overridable at build time via -ldflags -X so a dev
// install (omni-dev, :8788) can coexist with prod without sharing port,
// config or db. The version is omni-wide, shared with the server.
var (
	app         = "omni"
	defaultAddr = ":8787"
)

const version = omniversion.Version

type command struct {
	name    string // help | status | list | detail | connect | llm-* | pairing-*
	channel string // channel name, or llm provider for the llm-* commands
	topic   string // for help: "" (root) | status | channels | connect | llm | llm-connect | pairing
	arg     string // pairing-approve: the code; pairing-revoke: the user id
	model   string // llm-model: the model id
	effort  string // llm-model: low | medium | high
}

func route(args []string) (command, error) {
	if len(args) == 0 {
		return command{name: "help"}, nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		return command{name: "help"}, nil
	case "status":
		if len(args) == 1 {
			return command{name: "status"}, nil
		}
		switch args[1] {
		case "--help", "-h":
			return command{name: "help", topic: "status"}, nil
		}
		return command{}, fmt.Errorf("unknown argument %q — try `omni status --help`", args[1])
	case "doctor":
		if len(args) == 1 {
			return command{name: "doctor"}, nil
		}
		switch args[1] {
		case "--help", "-h":
			return command{name: "help", topic: "doctor"}, nil
		}
		return command{}, fmt.Errorf("unknown argument %q — try `omni doctor --help`", args[1])
	case "channels":
		if len(args) == 1 {
			return command{name: "list"}, nil
		}
		switch args[1] {
		case "--help", "-h":
			return command{name: "help", topic: "channels"}, nil
		case "connect":
			fs := flag.NewFlagSet("connect", flag.ContinueOnError)
			fs.SetOutput(io.Discard) // we print our own help, not flag's usage dump
			c := fs.String("c", "", "channel to connect")
			if err := fs.Parse(args[2:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return command{name: "help", topic: "connect"}, nil
				}
				return command{}, err
			}
			return command{name: "connect", channel: *c}, nil
		}
		return command{name: "detail", channel: args[1]}, nil
	case "llm":
		if len(args) == 1 {
			return command{name: "llm-list"}, nil
		}
		switch args[1] {
		case "--help", "-h":
			return command{name: "help", topic: "llm"}, nil
		case "connect":
			fs := flag.NewFlagSet("connect", flag.ContinueOnError)
			fs.SetOutput(io.Discard) // we print our own help, not flag's usage dump
			p := fs.String("p", "", "provider to connect")
			if err := fs.Parse(args[2:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return command{name: "help", topic: "llm-connect"}, nil
				}
				return command{}, err
			}
			return command{name: "llm-connect", channel: *p}, nil
		case "set-default":
			fs := flag.NewFlagSet("set-default", flag.ContinueOnError)
			fs.SetOutput(io.Discard) // we print our own help, not flag's usage dump
			p := fs.String("p", "", "provider to make the default")
			if err := fs.Parse(args[2:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return command{name: "help", topic: "llm-set-default"}, nil
				}
				return command{}, err
			}
			return command{name: "llm-set-default", channel: *p}, nil
		case "model":
			fs := flag.NewFlagSet("model", flag.ContinueOnError)
			fs.SetOutput(io.Discard) // we print our own help, not flag's usage dump
			p := fs.String("p", "", "provider")
			m := fs.String("m", "", "model to use")
			e := fs.String("e", "", "reasoning effort: low | medium | high")
			if err := fs.Parse(args[2:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return command{name: "help", topic: "llm-model"}, nil
				}
				return command{}, err
			}
			return command{name: "llm-model", channel: *p, model: *m, effort: *e}, nil
		}
		return command{name: "llm-detail", channel: args[1]}, nil
	case "pairing":
		if len(args) == 1 {
			return command{name: "pairing-list"}, nil
		}
		switch args[1] {
		case "--help", "-h":
			return command{name: "help", topic: "pairing"}, nil
		case "approve", "revoke":
			rest := args[2:]
			if slices.Contains(rest, "--help") || slices.Contains(rest, "-h") {
				return command{name: "help", topic: "pairing"}, nil
			}
			what := map[string]string{"approve": "<code>", "revoke": "<user-id>"}[args[1]]
			if len(rest) != 2 || rest[0] != "telegram" {
				return command{}, fmt.Errorf("usage: omni pairing %s telegram %s", args[1], what)
			}
			return command{name: "pairing-" + args[1], channel: rest[0], arg: rest[1]}, nil
		}
		return command{}, fmt.Errorf("unknown subcommand %q — try `omni pairing --help`", args[1])
	case "guardian":
		if len(args) == 1 {
			return command{name: "guardian-status"}, nil
		}
		switch args[1] {
		case "--help", "-h":
			return command{name: "help", topic: "guardian"}, nil
		case "set-interval":
			rest := args[2:]
			if slices.Contains(rest, "--help") || slices.Contains(rest, "-h") {
				return command{name: "help", topic: "guardian"}, nil
			}
			if len(rest) != 1 {
				return command{}, fmt.Errorf("usage: omni guardian set-interval <duration>  (e.g. 2m, 15m, 1h)")
			}
			return command{name: "guardian-interval", arg: rest[0]}, nil
		}
		fs := flag.NewFlagSet("guardian", flag.ContinueOnError)
		fs.SetOutput(io.Discard) // we print our own help, not flag's usage dump
		en := fs.String("enabled", "", "true|false")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return command{name: "help", topic: "guardian"}, nil
			}
			return command{}, err
		}
		switch *en {
		case "true", "false":
			return command{name: "guardian-enable", arg: *en}, nil
		case "":
			return command{}, fmt.Errorf("unknown subcommand %q — try `omni guardian --help`", args[1])
		}
		return command{}, fmt.Errorf("--enabled must be true or false")
	}
	return command{}, fmt.Errorf("unknown command %q — try `omni help`", args[0])
}

func serverURL() string {
	a := os.Getenv("OMNI_ADDR")
	switch {
	case a == "":
		return "http://localhost" + defaultAddr
	case strings.HasPrefix(a, ":"):
		return "http://localhost" + a
	case strings.HasPrefix(a, "http://"), strings.HasPrefix(a, "https://"):
		return a
	default:
		return "http://" + a
	}
}

// saveConfigKey merges one key into ~/.config/omni/config.yaml (0600),
// keeping the other keys intact.
func saveConfigKey(key, value string) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "config.yaml")
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		yaml.Unmarshal(data, &cfg) // unreadable config: start fresh
	}
	cfg[key] = value
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

var llmProviderNames = []string{"openai", "claude", "gemini"}

// llmConfigKey maps a provider to its config.yaml key (server reads the same).
var llmConfigKey = map[string]string{
	"openai": "openai_key",
	"claude": "anthropic_key",
	"gemini": "gemini_key",
}

func renderLLM(l LLM) string {
	var s string
	if l.Connected {
		s = okStyle.Render("●") + " " + l.Name + " — connected"
		if l.Source != "" {
			s += dimStyle.Render(" (" + l.Source + ")")
		}
	} else {
		s = dimStyle.Render("○ " + l.Name + " — disconnected")
	}
	if l.Model != "" {
		s += " · " + l.Model
		if l.Effort != "" {
			s += dimStyle.Render(" (effort " + l.Effort + ")")
		}
	}
	if l.Default {
		s += " " + selectedStyle.Render("★ default")
	}
	if l.BudgetNote != "" {
		s += "\n    " + warnStyle.Render("! "+l.BudgetNote)
	}
	return s
}

func renderPairing(p Pairing) string {
	if p.Approved {
		return okStyle.Render("●") + " " + p.UserID + " — paired"
	}
	return dimStyle.Render("○ "+p.UserID+" — pending") + " · approve with code " + selectedStyle.Render(p.Code)
}

func renderChannel(ch Channel) string {
	if ch.Connected {
		s := okStyle.Render("●") + " " + ch.Name + " — connected"
		if ch.BotUsername != "" {
			s += dimStyle.Render(" (@" + ch.BotUsername + ")")
		}
		return s
	}
	return dimStyle.Render("○ " + ch.Name + " — disconnected")
}

func fail(err error) int {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		fmt.Fprintln(os.Stderr, errStyle.Render("cannot reach the omni server at "+serverURL()))
		fmt.Fprintln(os.Stderr, helpStyle.Render("is it running? start it with: "+app+"-server"))
		return 1
	}
	fmt.Fprintln(os.Stderr, errStyle.Render(err.Error()))
	return 1
}

func runConnect(c *Client, channel string) int {
	if channel == "" {
		final, err := tea.NewProgram(newSelectModel("Connect a channel", []string{"telegram"})).Run()
		if err != nil {
			return fail(err)
		}
		channel = final.(selectModel).choice
		if channel == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
	}

	ch, err := c.Connect(channel, "")
	if errors.Is(err, ErrTokenRequired) {
		final, terr := tea.NewProgram(newTokenModel("Telegram bot token", "123456:ABC-DEF...",
			"paste your BotFather token · enter confirm · esc cancel")).Run()
		if terr != nil {
			return fail(terr)
		}
		token := final.(tokenModel).Token()
		if token == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
		if serr := saveConfigKey("telegram_token", token); serr != nil {
			return fail(serr)
		}
		fmt.Println(dimStyle.Render("token saved to ~/.config/" + app + "/config.yaml"))
		ch, err = c.Connect(channel, token)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Println(okStyle.Render("✓") + " " + ch.Name + " connected as " + selectedStyle.Render("@"+ch.BotUsername))
	return 0
}

func runLLMConnect(c *Client, provider string) int {
	if provider == "" {
		final, err := tea.NewProgram(newSelectModel("Connect an llm provider", llmProviderNames)).Run()
		if err != nil {
			return fail(err)
		}
		provider = final.(selectModel).choice
		if provider == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
	}

	l, err := c.ConnectLLM(provider, "")
	if errors.Is(err, ErrKeyRequired) {
		final, terr := tea.NewProgram(newTokenModel(provider+" API key", "sk-...",
			"paste your API key · enter confirm · esc cancel")).Run()
		if terr != nil {
			return fail(terr)
		}
		key := final.(tokenModel).Token()
		if key == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
		if serr := saveConfigKey(llmConfigKey[provider], key); serr != nil {
			return fail(serr)
		}
		fmt.Println(dimStyle.Render("key saved to ~/.config/" + app + "/config.yaml"))
		l, err = c.ConnectLLM(provider, key)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Println(okStyle.Render("✓") + " " + l.Name + " connected " + dimStyle.Render("("+l.Source+")"))
	return 0
}

// runLLMSetDefault saves the default provider to config.yaml; the server only
// reads it, so no API call is needed.
func runLLMSetDefault(provider string) int {
	if provider == "" {
		final, err := tea.NewProgram(newSelectModel("Default llm provider", llmProviderNames)).Run()
		if err != nil {
			return fail(err)
		}
		provider = final.(selectModel).choice
		if provider == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
	}
	if !slices.Contains(llmProviderNames, provider) {
		fmt.Fprintln(os.Stderr, errStyle.Render(fmt.Sprintf("unknown provider %q — one of: %s", provider, strings.Join(llmProviderNames, ", "))))
		return 2
	}
	if err := saveConfigKey("default_llm", provider); err != nil {
		return fail(err)
	}
	fmt.Println(okStyle.Render("✓") + " " + provider + " is now the default llm")
	return 0
}

var llmEfforts = []string{"low", "medium", "high"}

// runLLMModel picks a provider's model (and optional effort) and saves them to
// config.yaml as <provider>_model / <provider>_effort; the server only reads
// them, but the model list comes from the server, which needs to be running.
func runLLMModel(c *Client, provider, model, effort string) int {
	if effort != "" && !slices.Contains(llmEfforts, effort) {
		fmt.Fprintln(os.Stderr, errStyle.Render(fmt.Sprintf("unknown effort %q — one of: %s", effort, strings.Join(llmEfforts, ", "))))
		return 2
	}
	if provider == "" {
		final, err := tea.NewProgram(newSelectModel("Model for which provider", llmProviderNames)).Run()
		if err != nil {
			return fail(err)
		}
		provider = final.(selectModel).choice
		if provider == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
	}
	if !slices.Contains(llmProviderNames, provider) {
		fmt.Fprintln(os.Stderr, errStyle.Render(fmt.Sprintf("unknown provider %q — one of: %s", provider, strings.Join(llmProviderNames, ", "))))
		return 2
	}
	models, err := c.LLMModels(provider)
	if err != nil {
		return fail(err)
	}
	if model == "" {
		final, err := tea.NewProgram(newSelectModel(provider+" model", models)).Run()
		if err != nil {
			return fail(err)
		}
		model = final.(selectModel).choice
		if model == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
	}
	if !slices.Contains(models, model) {
		fmt.Fprintln(os.Stderr, errStyle.Render(fmt.Sprintf("unknown model %q for %s — one of: %s", model, provider, strings.Join(models, ", "))))
		return 2
	}
	if err := saveConfigKey(provider+"_model", model); err != nil {
		return fail(err)
	}
	out := okStyle.Render("✓") + " " + provider + " model set to " + selectedStyle.Render(model)
	if effort != "" {
		if err := saveConfigKey(provider+"_effort", effort); err != nil {
			return fail(err)
		}
		out += dimStyle.Render(" · effort " + effort)
	}
	fmt.Println(out)
	return 0
}

// The guardian (<app>-guardian binary) runs as a systemd user timer; these
// subcommands just drive systemctl locally — no server API involved.

func guardianTimer() string { return app + "-guardian.timer" }

// dataDir is ~/.local/share/<app> (respects XDG_DATA_HOME).
func dataDir() string {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, app)
}

// guardianAlerts reads the guardian's persisted red checks (name → red-since).
func guardianAlerts() map[string]string {
	st := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(dataDir(), "guardian.json")); err == nil {
		json.Unmarshal(data, &st)
	}
	return st
}

func systemctlUser(args ...string) (string, error) {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runGuardianStatus() int {
	timer := guardianTimer()
	switch enabled, _ := systemctlUser("is-enabled", timer); enabled {
	case "enabled":
		fmt.Println(okStyle.Render("●") + " " + timer + " — enabled")
		if line, err := systemctlUser("list-timers", timer, "--no-pager", "--no-legend"); err == nil && line != "" {
			fmt.Println(dimStyle.Render("  " + line))
		}
	case "disabled":
		fmt.Println(dimStyle.Render("○ "+timer+" — disabled") + " · re-arm with " + cmdStyle.Render("omni guardian --enabled=true"))
	default:
		fmt.Println(dimStyle.Render("○ " + timer + " — not installed (run scripts/install.sh)"))
	}

	// active alerts = the guardian's persisted red checks
	st := guardianAlerts()
	if len(st) == 0 {
		fmt.Println(okStyle.Render("●") + " no active alerts")
		return 0
	}
	for _, name := range slices.Sorted(maps.Keys(st)) {
		fmt.Println(warnStyle.Render("! " + name + " — red since " + st[name]))
	}
	return 0
}

func runGuardianInterval(arg string) int {
	d, err := time.ParseDuration(arg)
	if err != nil || d < 30*time.Second {
		fmt.Fprintln(os.Stderr, errStyle.Render("interval must be a duration of at least 30s (e.g. 2m, 15m, 1h)"))
		return 2
	}
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return fail(err)
	}
	// drop-in override: survives install.sh rewriting the base timer unit
	dropDir := filepath.Join(cfgDir, "systemd", "user", guardianTimer()+".d")
	if err := os.MkdirAll(dropDir, 0o755); err != nil {
		return fail(err)
	}
	// the empty assignment resets ALL On*Sec clauses, including the base
	// unit's OnActiveSec bootstrap — re-add it, or the timer never fires
	// again after a restart (OnUnitActiveSec alone waits for a service
	// activation that never comes)
	conf := "[Timer]\nOnUnitActiveSec=\nOnActiveSec=2min\nOnUnitActiveSec=" + arg + "\n"
	if err := os.WriteFile(filepath.Join(dropDir, "override.conf"), []byte(conf), 0o644); err != nil {
		return fail(err)
	}
	if out, err := systemctlUser("daemon-reload"); err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render("systemctl daemon-reload failed: "+out))
		return 1
	}
	if out, err := systemctlUser("restart", guardianTimer()); err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render("could not restart "+guardianTimer()+": "+out+" — is the guardian installed?"))
		return 1
	}
	fmt.Println(okStyle.Render("✓") + " guardian checks every " + selectedStyle.Render(arg))
	return 0
}

func runGuardianEnable(on bool) int {
	action := "disable"
	if on {
		action = "enable"
	}
	if out, err := systemctlUser(action, "--now", guardianTimer()); err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render("systemctl "+action+" failed: "+out+" — is the guardian installed? (scripts/install.sh)"))
		return 1
	}
	if on {
		fmt.Println(okStyle.Render("✓") + " guardian enabled — checks resume on schedule")
	} else {
		fmt.Println(okStyle.Render("✓") + " guardian disabled — no checks, no alerts until re-enabled")
	}
	return 0
}

func run(args []string) int {
	cmd, err := route(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, errStyle.Render(err.Error()))
		return 2
	}
	c := NewClient(serverURL())
	switch cmd.name {
	case "help":
		switch cmd.topic {
		case "channels":
			fmt.Print(helpChannels())
		case "connect":
			fmt.Print(helpConnect())
		case "status":
			fmt.Print(helpStatus())
		case "doctor":
			fmt.Print(helpDoctor())
		case "llm":
			fmt.Print(helpLLM())
		case "llm-connect":
			fmt.Print(helpLLMConnect())
		case "llm-set-default":
			fmt.Print(helpLLMSetDefault())
		case "llm-model":
			fmt.Print(helpLLMModel())
		case "pairing":
			fmt.Print(helpPairing())
		case "guardian":
			fmt.Print(helpGuardian())
		default:
			fmt.Print(helpText())
		}
	case "status":
		return runStatus(c)
	case "doctor":
		return runDoctor()
	case "list":
		chs, err := c.Channels()
		if err != nil {
			return fail(err)
		}
		for _, ch := range chs {
			fmt.Println(renderChannel(ch))
		}
	case "detail":
		ch, err := c.Channel(cmd.channel)
		if err != nil {
			return fail(err)
		}
		fmt.Println(renderChannel(ch))
	case "connect":
		return runConnect(c, cmd.channel)
	case "llm-list":
		ls, err := c.LLMs()
		if err != nil {
			return fail(err)
		}
		for _, l := range ls {
			fmt.Println(renderLLM(l))
		}
	case "llm-detail":
		l, err := c.LLM(cmd.channel)
		if err != nil {
			return fail(err)
		}
		fmt.Println(renderLLM(l))
	case "llm-connect":
		return runLLMConnect(c, cmd.channel)
	case "llm-set-default":
		return runLLMSetDefault(cmd.channel)
	case "llm-model":
		return runLLMModel(c, cmd.channel, cmd.model, cmd.effort)
	case "pairing-list":
		ps, err := c.Pairings("telegram")
		if err != nil {
			return fail(err)
		}
		if len(ps) == 0 {
			fmt.Println(dimStyle.Render("no pairings yet — message the bot to get a code"))
			return 0
		}
		for _, p := range ps {
			fmt.Println(renderPairing(p))
		}
	case "pairing-approve":
		p, err := c.ApprovePairing(cmd.channel, cmd.arg)
		if err != nil {
			return fail(err)
		}
		fmt.Println(okStyle.Render("✓") + " approved " + selectedStyle.Render(p.UserID) + " — they can talk to the bot now")
	case "pairing-revoke":
		if err := c.RevokePairing(cmd.channel, cmd.arg); err != nil {
			return fail(err)
		}
		fmt.Println(okStyle.Render("✓") + " revoked " + selectedStyle.Render(cmd.arg))
	case "guardian-status":
		return runGuardianStatus()
	case "guardian-interval":
		return runGuardianInterval(cmd.arg)
	case "guardian-enable":
		return runGuardianEnable(cmd.arg == "true")
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}
