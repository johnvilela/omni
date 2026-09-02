package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
		{[]string{"llm", "model"}, "llm-model", "", "", false},
		{[]string{"llm", "model", "-p", "openai"}, "llm-model", "openai", "", false},
		{[]string{"llm", "model", "--help"}, "help", "", "llm-model", false},
		{[]string{"pairing"}, "pairing-list", "", "", false},
		{[]string{"pairing", "--help"}, "help", "", "pairing", false},
		{[]string{"pairing", "-h"}, "help", "", "pairing", false},
		{[]string{"pairing", "approve", "--help"}, "help", "", "pairing", false},
		{[]string{"pairing", "approve", "telegram"}, "", "", "", true},
		{[]string{"pairing", "approve", "discord", "X"}, "", "", "", true},
		{[]string{"pairing", "junk"}, "", "", "", true},
		{[]string{"status"}, "status", "", "", false},
		{[]string{"status", "--help"}, "help", "", "status", false},
		{[]string{"status", "-h"}, "help", "", "status", false},
		{[]string{"status", "junk"}, "", "", "", true},
		{[]string{"doctor"}, "doctor", "", "", false},
		{[]string{"doctor", "--help"}, "help", "", "doctor", false},
		{[]string{"doctor", "-h"}, "help", "", "doctor", false},
		{[]string{"doctor", "junk"}, "", "", "", true},
		{[]string{"guardian"}, "guardian-status", "", "", false},
		{[]string{"guardian", "--help"}, "help", "", "guardian", false},
		{[]string{"guardian", "--enabled=false"}, "guardian-enable", "", "", false},
		{[]string{"guardian", "--enabled=maybe"}, "", "", "", true},
		{[]string{"guardian", "set-interval"}, "", "", "", true},
		{[]string{"guardian", "junk"}, "", "", "", true},
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

	// approve/revoke carry the code / user id in cmd.arg
	cmd, err := route([]string{"pairing", "approve", "telegram", "CODE1234"})
	if err != nil || cmd.name != "pairing-approve" || cmd.channel != "telegram" || cmd.arg != "CODE1234" {
		t.Errorf("route(pairing approve) = %+v, %v", cmd, err)
	}
	cmd, err = route([]string{"pairing", "revoke", "telegram", "99"})
	if err != nil || cmd.name != "pairing-revoke" || cmd.channel != "telegram" || cmd.arg != "99" {
		t.Errorf("route(pairing revoke) = %+v, %v", cmd, err)
	}

	// llm model carries model and effort in their own fields
	cmd, err = route([]string{"llm", "model", "-p", "openai", "-m", "gpt-test", "-e", "high"})
	if err != nil || cmd.name != "llm-model" || cmd.channel != "openai" || cmd.model != "gpt-test" || cmd.effort != "high" {
		t.Errorf("route(llm model -p -m -e) = %+v, %v", cmd, err)
	}

	// guardian set-interval carries the duration in cmd.arg
	cmd, err = route([]string{"guardian", "set-interval", "5m"})
	if err != nil || cmd.name != "guardian-interval" || cmd.arg != "5m" {
		t.Errorf("route(guardian set-interval) = %+v, %v", cmd, err)
	}
	cmd, err = route([]string{"guardian", "--enabled=true"})
	if err != nil || cmd.name != "guardian-enable" || cmd.arg != "true" {
		t.Errorf("route(guardian --enabled=true) = %+v, %v", cmd, err)
	}
}

func TestRenderPairing(t *testing.T) {
	ok := renderPairing(Pairing{UserID: "99", Code: "CODE1234", Approved: true})
	for _, want := range []string{"99", "paired"} {
		if !strings.Contains(ok, want) {
			t.Errorf("paired row missing %q in %q", want, ok)
		}
	}
	if strings.Contains(ok, "CODE1234") {
		t.Errorf("paired row should not show the code: %q", ok)
	}
	pend := renderPairing(Pairing{UserID: "7", Code: "CODE1234"})
	for _, want := range []string{"7", "pending", "CODE1234"} {
		if !strings.Contains(pend, want) {
			t.Errorf("pending row missing %q in %q", want, pend)
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

	dir := filepath.Join(tmp, "omni")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("token_budget: 9000\n"), 0o600); err != nil {
		t.Fatal(err)
	}

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
	var cfg map[string]any
	if yaml.Unmarshal(data, &cfg) != nil || cfg["telegram_token"] != "123:abc" || cfg["openai_key"] != "sk-test" {
		t.Fatalf("config content = %q, want both keys to survive", data)
	}
	if cfg["token_budget"] != 9000 {
		t.Fatalf("token_budget = %v, want the int key to survive rewrites; config = %q", cfg["token_budget"], data)
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
	withModel := renderLLM(LLM{Name: "claude", Connected: true, Source: "oauth", Model: "claude-test", Effort: "high"})
	for _, want := range []string{"claude-test", "effort high"} {
		if !strings.Contains(withModel, want) {
			t.Errorf("renderLLM with model missing %q in %q", want, withModel)
		}
	}
	if strings.Contains(got, "effort") {
		t.Errorf("renderLLM without model/effort should not mention effort: %q", got)
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

func TestRunLLMModel(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/llm/openai/models" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `["gpt-test","gpt-other"]`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)

	readCfg := func() map[string]string {
		data, _ := os.ReadFile(filepath.Join(tmp, "omni", "config.yaml"))
		var cfg map[string]string
		yaml.Unmarshal(data, &cfg)
		return cfg
	}

	if code := runLLMModel(c, "openai", "gpt-test", "high"); code != 0 {
		t.Fatalf("valid model+effort = %d, want 0", code)
	}
	if cfg := readCfg(); cfg["openai_model"] != "gpt-test" || cfg["openai_effort"] != "high" {
		t.Fatalf("config = %v, want openai_model and openai_effort saved", cfg)
	}

	if code := runLLMModel(c, "openai", "gpt-other", ""); code != 0 {
		t.Fatalf("valid model without effort = %d, want 0", code)
	}
	if cfg := readCfg(); cfg["openai_model"] != "gpt-other" || cfg["openai_effort"] != "high" {
		t.Fatalf("config = %v, want model updated and prior effort untouched", cfg)
	}

	if code := runLLMModel(c, "openai", "bogus", ""); code != 2 {
		t.Fatalf("unknown model = %d, want 2", code)
	}
	if cfg := readCfg(); cfg["openai_model"] != "gpt-other" {
		t.Fatalf("unknown model must not overwrite the config; got %v", cfg)
	}

	if code := runLLMModel(c, "cohere", "gpt-test", ""); code != 2 {
		t.Fatalf("unknown provider = %d, want 2", code)
	}
	if code := runLLMModel(c, "openai", "gpt-test", "extreme"); code != 2 {
		t.Fatalf("unknown effort = %d, want 2", code)
	}
}

func TestHelpScreens(t *testing.T) {
	root := helpText()
	for _, want := range []string{"omni channels", "omni status", "omni doctor", "omni llm", "omni pairing", "omni guardian", "--help", "-server", version} {
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

	dr := helpDoctor()
	for _, want := range []string{"omni doctor", "install", "server down"} {
		if !strings.Contains(dr, want) {
			t.Errorf("doctor help missing %q", want)
		}
	}
	if strings.Contains(dr, "██") {
		t.Error("doctor help should not have the banner")
	}

	gu := helpGuardian()
	for _, want := range []string{"set-interval", "--enabled=false", "journalctl"} {
		if !strings.Contains(gu, want) {
			t.Errorf("guardian help missing %q", want)
		}
	}
	if strings.Contains(gu, "██") {
		t.Error("guardian help should not have the banner")
	}

	pr := helpPairing()
	for _, want := range []string{"approve", "revoke", "omni pairing approve telegram"} {
		if !strings.Contains(pr, want) {
			t.Errorf("pairing help missing %q", want)
		}
	}
	if strings.Contains(pr, "██") {
		t.Error("pairing help should not have the banner")
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
	for _, want := range []string{"connect", "set-default", "model", "<provider>", "omni llm openai"} {
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

	lm := helpLLMModel()
	for _, want := range []string{"-p", "-m", "-e", "low", "high", "openai_model"} {
		if !strings.Contains(lm, want) {
			t.Errorf("llm model help missing %q", want)
		}
	}
	if strings.Contains(lm, "██") {
		t.Error("llm model help should not have the banner")
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
