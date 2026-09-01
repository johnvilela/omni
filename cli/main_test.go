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
		{[]string{"llm"}, "llm-list", "", "", false},
		{[]string{"llm", "--help"}, "help", "", "llm", false},
		{[]string{"llm", "-h"}, "help", "", "llm", false},
		{[]string{"llm", "openai"}, "llm-detail", "openai", "", false},
		{[]string{"llm", "connect"}, "llm-connect", "", "", false},
		{[]string{"llm", "connect", "-p", "gemini"}, "llm-connect", "gemini", "", false},
		{[]string{"llm", "connect", "--help"}, "help", "", "llm-connect", false},
		{[]string{"llm", "set-default"}, "llm-set-default", "", "", false},
		{[]string{"llm", "set-default", "-p", "openai"}, "llm-set-default", "openai", "", false},
		{[]string{"llm", "set-default", "--help"}, "help", "", "llm-set-default", false},
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

func TestSaveConfigKey(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := saveConfigKey("telegram_token", "123:abc"); err != nil {
		t.Fatalf("saveConfigKey: %v", err)
	}
	if err := saveConfigKey("openai_key", "sk-test"); err != nil {
		t.Fatalf("saveConfigKey second key: %v", err)
	}
	path := filepath.Join(tmp, "omni", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg map[string]string
	if yaml.Unmarshal(data, &cfg) != nil || cfg["telegram_token"] != "123:abc" || cfg["openai_key"] != "sk-test" {
		t.Fatalf("config content = %q, want both keys to survive", data)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestRenderStatus(t *testing.T) {
	chs := []Channel{{Name: "telegram", Connected: true, BotUsername: "omni_bot"}}
	llms := []LLM{
		{Name: "openai", Connected: true, Source: "api_key"},
		{Name: "claude", Connected: false},
	}

	up := renderStatus(ServerStatus{Version: version}, chs, llms)
	for _, want := range []string{"up", version, "telegram", "LLM", "openai", "api_key", "claude", "no alerts"} {
		if !strings.Contains(up, want) {
			t.Errorf("status (match) missing %q in:\n%s", want, up)
		}
	}

	mismatch := renderStatus(ServerStatus{Version: "other"}, chs, llms)
	for _, want := range []string{"version mismatch", "other", version} {
		if !strings.Contains(mismatch, want) {
			t.Errorf("status (mismatch) missing %q in:\n%s", want, mismatch)
		}
	}
	if strings.Contains(mismatch, "no alerts") {
		t.Error("status (mismatch) should not say no alerts")
	}
}

func TestRenderLLM(t *testing.T) {
	got := renderLLM(LLM{Name: "gemini", Connected: true, Source: "oauth"})
	for _, want := range []string{"gemini", "connected", "oauth"} {
		if !strings.Contains(got, want) {
			t.Errorf("renderLLM connected missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "default") {
		t.Errorf("renderLLM non-default should not mention default: %q", got)
	}
	off := renderLLM(LLM{Name: "gemini"})
	if !strings.Contains(off, "disconnected") || strings.Contains(off, "oauth") {
		t.Errorf("renderLLM disconnected = %q", off)
	}
	def := renderLLM(LLM{Name: "claude", Connected: true, Source: "oauth", Default: true})
	if !strings.Contains(def, "default") {
		t.Errorf("renderLLM default missing marker: %q", def)
	}
}

func TestRunLLMSetDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := saveConfigKey("openai_key", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if code := runLLMSetDefault("gemini"); code != 0 {
		t.Fatalf("runLLMSetDefault(gemini) = %d, want 0", code)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "omni", "config.yaml"))
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	var cfg map[string]string
	if yaml.Unmarshal(data, &cfg) != nil || cfg["default_llm"] != "gemini" || cfg["openai_key"] != "sk-test" {
		t.Fatalf("config content = %q, want default_llm alongside the key", data)
	}

	if code := runLLMSetDefault("cohere"); code == 0 {
		t.Fatal("runLLMSetDefault(cohere) = 0, want error")
	}
	data, _ = os.ReadFile(filepath.Join(tmp, "omni", "config.yaml"))
	cfg = nil
	if yaml.Unmarshal(data, &cfg) != nil || cfg["default_llm"] != "gemini" {
		t.Fatalf("unknown provider must not overwrite the default; config = %q", data)
	}
}

func TestHelpScreens(t *testing.T) {
	root := helpText()
	for _, want := range []string{"omni channels", "omni status", "omni llm", "--help", "-server", version} {
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

	llm := helpLLM()
	for _, want := range []string{"connect", "set-default", "<provider>", "omni llm openai"} {
		if !strings.Contains(llm, want) {
			t.Errorf("llm help missing %q", want)
		}
	}
	if strings.Contains(llm, "██") {
		t.Error("llm help should not have the banner")
	}

	sd := helpLLMSetDefault()
	for _, want := range []string{"-p", "set-default -p openai", "default_llm"} {
		if !strings.Contains(sd, want) {
			t.Errorf("set-default help missing %q", want)
		}
	}
	if strings.Contains(sd, "██") {
		t.Error("set-default help should not have the banner")
	}

	lc := helpLLMConnect()
	for _, want := range []string{"-p", "connect -p gemini", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"} {
		if !strings.Contains(lc, want) {
			t.Errorf("llm connect help missing %q", want)
		}
	}
	if strings.Contains(lc, "██") {
		t.Error("llm connect help should not have the banner")
	}
}
