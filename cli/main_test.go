package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRoute(t *testing.T) {
	cases := []struct {
		args    []string
		name    string
		channel string
		topic   string
		wantErr bool
	}{
		{[]string{}, "help", "", "", false},
		{[]string{"help"}, "help", "", "", false},
		{[]string{"--help"}, "help", "", "", false},
		{[]string{"-h"}, "help", "", "", false},
		{[]string{"channels"}, "list", "", "", false},
		{[]string{"channels", "--help"}, "help", "", "channels", false},
		{[]string{"channels", "-h"}, "help", "", "channels", false},
		{[]string{"channels", "telegram"}, "detail", "telegram", "", false},
		{[]string{"channels", "connect"}, "connect", "", "", false},
		{[]string{"channels", "connect", "-c", "telegram"}, "connect", "telegram", "", false},
		{[]string{"channels", "connect", "--help"}, "help", "", "connect", false},
		{[]string{"channels", "connect", "-c", "telegram", "-h"}, "help", "", "connect", false},
		{[]string{"status"}, "status", "", "", false},
		{[]string{"status", "--help"}, "help", "", "status", false},
		{[]string{"status", "-h"}, "help", "", "status", false},
		{[]string{"status", "junk"}, "", "", "", true},
		{[]string{"frobnicate"}, "", "", "", true},
	}
	for _, c := range cases {
		cmd, err := route(c.args)
		if c.wantErr != (err != nil) {
			t.Errorf("route(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && (cmd.name != c.name || cmd.channel != c.channel || cmd.topic != c.topic) {
			t.Errorf("route(%v) = %+v, want {%s %s %s}", c.args, cmd, c.name, c.channel, c.topic)
		}
	}
}

func TestServerURL(t *testing.T) {
	t.Setenv("OMNI_ADDR", "")
	if got := serverURL(); got != "http://localhost:8787" {
		t.Fatalf("serverURL default = %q", got)
	}
	t.Setenv("OMNI_ADDR", ":9000")
	if got := serverURL(); got != "http://localhost:9000" {
		t.Fatalf("serverURL(:9000) = %q", got)
	}
	t.Setenv("OMNI_ADDR", "otherhost:9000")
	if got := serverURL(); got != "http://otherhost:9000" {
		t.Fatalf("serverURL(otherhost:9000) = %q", got)
	}
}

func TestSaveToken(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := saveToken("123:abc"); err != nil {
		t.Fatalf("saveToken: %v", err)
	}
	path := filepath.Join(tmp, "omni", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg struct {
		TelegramToken string `yaml:"telegram_token"`
	}
	if yaml.Unmarshal(data, &cfg) != nil || cfg.TelegramToken != "123:abc" {
		t.Fatalf("config content = %q", data)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestRenderStatus(t *testing.T) {
	chs := []Channel{{Name: "telegram", Connected: true, BotUsername: "omni_bot"}}

	// server version matches the CLI's own (version = "dev" in tests)
	up := renderStatus(ServerStatus{Version: "dev"}, chs)
	for _, want := range []string{"up", "dev", "telegram", "no alerts"} {
		if !strings.Contains(up, want) {
			t.Errorf("status (match) missing %q in:\n%s", want, up)
		}
	}

	mismatch := renderStatus(ServerStatus{Version: "other"}, chs)
	for _, want := range []string{"version mismatch", "other", "dev"} {
		if !strings.Contains(mismatch, want) {
			t.Errorf("status (mismatch) missing %q in:\n%s", want, mismatch)
		}
	}
	if strings.Contains(mismatch, "no alerts") {
		t.Error("status (mismatch) should not say no alerts")
	}
}

func TestHelpScreens(t *testing.T) {
	root := helpText()
	for _, want := range []string{"omni channels", "omni status", "--help", "-server"} {
		if !strings.Contains(root, want) {
			t.Errorf("root help missing %q", want)
		}
	}
	if !strings.Contains(root, "██") {
		t.Error("root help missing ASCII art banner")
	}
	if strings.Contains(root, "-c telegram") {
		t.Error("root help should not list subcommand flags")
	}

	ch := helpChannels()
	for _, want := range []string{"connect", "<name>", "omni channels telegram"} {
		if !strings.Contains(ch, want) {
			t.Errorf("channels help missing %q", want)
		}
	}
	if strings.Contains(ch, "██") {
		t.Error("channels help should not have the banner")
	}

	st := helpStatus()
	for _, want := range []string{"omni status", "alert"} {
		if !strings.Contains(st, want) {
			t.Errorf("status help missing %q", want)
		}
	}
	if strings.Contains(st, "██") {
		t.Error("status help should not have the banner")
	}

	co := helpConnect()
	for _, want := range []string{"-c", "connect -c telegram", "TELEGRAM_BOT_TOKEN"} {
		if !strings.Contains(co, want) {
			t.Errorf("connect help missing %q", want)
		}
	}
	if strings.Contains(co, "██") {
		t.Error("connect help should not have the banner")
	}
}
