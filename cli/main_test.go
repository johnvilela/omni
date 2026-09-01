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
		wantErr bool
	}{
		{[]string{}, "help", "", false},
		{[]string{"help"}, "help", "", false},
		{[]string{"--help"}, "help", "", false},
		{[]string{"-h"}, "help", "", false},
		{[]string{"channels"}, "list", "", false},
		{[]string{"channels", "telegram"}, "detail", "telegram", false},
		{[]string{"channels", "connect"}, "connect", "", false},
		{[]string{"channels", "connect", "-c", "telegram"}, "connect", "telegram", false},
		{[]string{"frobnicate"}, "", "", true},
	}
	for _, c := range cases {
		cmd, err := route(c.args)
		if c.wantErr != (err != nil) {
			t.Errorf("route(%v) err = %v, wantErr %v", c.args, err, c.wantErr)
			continue
		}
		if err == nil && (cmd.name != c.name || cmd.channel != c.channel) {
			t.Errorf("route(%v) = %+v, want {%s %s}", c.args, cmd, c.name, c.channel)
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

func TestHelpMentionsCommands(t *testing.T) {
	h := helpText()
	for _, want := range []string{"omni channels", "connect", "-c", "help"} {
		if !strings.Contains(h, want) {
			t.Errorf("help missing %q", want)
		}
	}
	if !strings.Contains(h, "██") {
		t.Error("help missing ASCII art banner")
	}
}
