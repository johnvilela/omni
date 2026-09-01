package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

// app and defaultAddr are overridable at build time via -ldflags -X so a dev
// install (omni-dev, :8788) can coexist with prod without sharing port,
// config or db. version belongs to the cli alone — bump it when the cli
// changes; the server has its own.
var (
	app         = "omni"
	defaultAddr = ":8787"
	version     = "v0.1.0"
)

type command struct {
	name    string // help | status | list | detail | connect
	channel string
	topic   string // for help: "" (root) | channels | connect
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

// saveToken writes the bot token to ~/.config/omni/config.yaml (0600).
func saveToken(token string) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(map[string]string{"telegram_token": token})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o600)
}

func renderChannel(ch Channel) string {
	if ch.Connected {
		s := okStyle.Render("●") + " " + ch.Name + " — connected"
		if ch.BotUsername != "" {
			s += dimStyle.Render(" (@" + ch.BotUsername + ")")
		}
		return s
	}
	return dimStyle.Render("○ "+ch.Name+" — disconnected")
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
		final, terr := tea.NewProgram(newTokenModel()).Run()
		if terr != nil {
			return fail(terr)
		}
		token := final.(tokenModel).Token()
		if token == "" {
			fmt.Println(dimStyle.Render("canceled"))
			return 1
		}
		if serr := saveToken(token); serr != nil {
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
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:]))
}
