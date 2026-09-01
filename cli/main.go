package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	cfg := map[string]string{}
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
	if l.Default {
		s += " " + selectedStyle.Render("★ default")
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
		case "llm":
			fmt.Print(helpLLM())
		case "llm-connect":
			fmt.Print(helpLLMConnect())
		case "llm-set-default":
			fmt.Print(helpLLMSetDefault())
		case "pairing":
			fmt.Print(helpPairing())
		default:
			fmt.Print(helpText())
		}
	case "status":
		return runStatus(c)
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
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}
