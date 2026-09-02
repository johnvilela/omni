package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// llmStatus is the wire status of one LLM provider.
type llmStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Source    string `json:"source,omitempty"` // oauth | api_key | claude-code
	Default   bool   `json:"default,omitempty"`
	Model     string `json:"model,omitempty"`  // user-picked via `omni llm model`
	Effort    string `json:"effort,omitempty"` // low | medium | high
	// BudgetNote warns that the configured token_budget exceeds this
	// provider's chat-model window and got clamped.
	BudgetNote string `json:"budget_note,omitempty"`
}

var llmProviders = []string{"openai", "claude", "gemini"}

// llmFallbackModels are served when a provider has no api key to list models
// with (oauth/claude-code sources, or disconnected) or the live fetch fails.
// ponytail: curated names go stale — refresh when providers ship new
// generations. Each name must be valid for both the HTTP API and the vendor
// CLI's model flag.
var llmFallbackModels = map[string][]string{
	"openai": {"gpt-5.1", "gpt-5.1-codex", "gpt-5", "gpt-5-mini", "gpt-4.1"},
	"claude": {"claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5", "claude-3-5-haiku-latest"},
	"gemini": {"gemini-3-pro-preview", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash"},
}

// llmCodexModels replaces the openai fallback when the source is oauth: those
// answers go through the codex CLI, which on ChatGPT logins accepts only its
// own picker's models — api-only names like gpt-4.1 get a 400.
// ponytail: codex has no list-models command; this mirrors its /model picker
// (codex v0.151) and must be refreshed when codex ships new generations.
var llmCodexModels = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini"}

var errKeyRequired = apiError{"key_required"}

var llmHTTP = &http.Client{Timeout: 15 * time.Second}

func readJSONFile(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

// hasVendorCreds reports whether the provider's own CLI (codex, claude code,
// gemini-cli) has a stored login this machine can reuse.
// ponytail: presence+parse only — these tokens expire and refreshing them is
// the vendor CLI's job; omni just reuses what a vendor login left behind.
func hasVendorCreds(provider string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	switch provider {
	case "openai":
		var c struct {
			APIKey string          `json:"OPENAI_API_KEY"`
			Tokens json.RawMessage `json:"tokens"`
		}
		return readJSONFile(filepath.Join(home, ".codex", "auth.json"), &c) &&
			(c.APIKey != "" || (len(c.Tokens) > 0 && string(c.Tokens) != "null"))
	case "claude":
		var c struct {
			OAuth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		return readJSONFile(filepath.Join(home, ".claude", ".credentials.json"), &c) &&
			c.OAuth.AccessToken != ""
	case "gemini":
		var c struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		return readJSONFile(filepath.Join(home, ".gemini", "oauth_creds.json"), &c) &&
			(c.AccessToken != "" || c.RefreshToken != "")
	}
	return false
}

// resolveLLM picks a provider's credential source: explicit request key, then
// a vendor CLI login (oauth), then env, then config.yaml, then (claude only)
// the claude binary itself. The key is returned only for api_key sources.
func resolveLLM(provider, reqKey string) (source, key string) {
	if reqKey != "" {
		return "api_key", reqKey
	}
	if hasVendorCreds(provider) {
		return "oauth", ""
	}
	env := map[string]string{"openai": "OPENAI_API_KEY", "claude": "ANTHROPIC_API_KEY", "gemini": "GEMINI_API_KEY"}
	if k := os.Getenv(env[provider]); k != "" {
		return "api_key", k
	}
	cfg := readConfig()
	if k := map[string]string{"openai": cfg.OpenAIKey, "claude": cfg.AnthropicKey, "gemini": cfg.GeminiKey}[provider]; k != "" {
		return "api_key", k
	}
	if provider == "claude" {
		if _, err := exec.LookPath("claude"); err == nil {
			return "claude-code", ""
		}
	}
	return "", ""
}

// llmAPIBase returns the provider's API base URL; OMNI_<PROVIDER>_API
// overrides it (used by tests, mirrors OMNI_TELEGRAM_API).
func llmAPIBase(provider string) string {
	env := map[string]string{"openai": "OMNI_OPENAI_API", "claude": "OMNI_CLAUDE_API", "gemini": "OMNI_GEMINI_API"}
	if b := os.Getenv(env[provider]); b != "" {
		return b
	}
	return map[string]string{
		"openai": "https://api.openai.com",
		"claude": "https://api.anthropic.com",
		"gemini": "https://generativelanguage.googleapis.com",
	}[provider]
}

// modelsRequest builds each provider's list-models call — the cheapest
// authenticated request they have, used both for key validation and for
// listing the models themselves.
func modelsRequest(ctx context.Context, provider, key string) (*http.Request, error) {
	base := llmAPIBase(provider)
	switch provider {
	case "openai":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		return req, err
	case "claude":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
		if err == nil {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		return req, err
	case "gemini":
		return http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1beta/models?key="+url.QueryEscape(key), nil)
	}
	return nil, fmt.Errorf("unknown provider %q", provider)
}

// validateLLMKey checks an api key by listing models.
func validateLLMKey(ctx context.Context, provider, key string) error {
	req, err := modelsRequest(ctx, provider, key)
	if err != nil {
		return err
	}
	resp, err := llmHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s rejected the key (%s)", provider, resp.Status)
	}
	return nil
}

// listLLMModels returns the chat models a provider offers: live from its API
// when an api key is in use, the curated fallback otherwise (oauth sources
// have no key to list with; fetch errors degrade to the fallback too).
func (s *Server) listLLMModels(ctx context.Context, provider string) []string {
	source, key := resolveLLM(provider, "")
	if source != "api_key" {
		if provider == "openai" && source == "oauth" {
			return llmCodexModels // oauth answers go through codex
		}
		return llmFallbackModels[provider]
	}
	req, err := modelsRequest(ctx, provider, key)
	if err != nil {
		return llmFallbackModels[provider]
	}
	resp, err := llmHTTP.Do(req)
	if err != nil {
		return llmFallbackModels[provider]
	}
	defer resp.Body.Close()
	// one struct covers all three shapes: openai/anthropic use data[].id,
	// gemini uses models[].name
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&r) != nil {
		return llmFallbackModels[provider]
	}
	var models []string
	for _, m := range r.Data {
		// ponytail: naive prefix filter — openai's list mixes in whisper/tts/
		// dall-e/embeddings and the CLI picker has no viewport; loosen if
		// someone needs an edge model.
		if provider == "openai" && !strings.HasPrefix(m.ID, "gpt-") &&
			!(len(m.ID) > 1 && m.ID[0] == 'o' && m.ID[1] >= '0' && m.ID[1] <= '9') {
			continue
		}
		models = append(models, m.ID)
	}
	for _, m := range r.Models {
		if !slices.Contains(m.Methods, "generateContent") {
			continue
		}
		models = append(models, strings.TrimPrefix(m.Name, "models/"))
	}
	if len(models) == 0 {
		return llmFallbackModels[provider]
	}
	return models
}

// llmStatuses reports every provider: connected means the user connected it
// AND its credentials still resolve; source is what would be used right now.
// With no default_llm configured and exactly one provider connected, that one
// is the implied default.
func (s *Server) llmStatuses() []llmStatus {
	cfg := readConfig()
	def := cfg.DefaultLLM
	models := map[string]string{"openai": cfg.OpenAIModel, "claude": cfg.ClaudeModel, "gemini": cfg.GeminiModel}
	efforts := map[string]string{"openai": cfg.OpenAIEffort, "claude": cfg.ClaudeEffort, "gemini": cfg.GeminiEffort}
	sts := make([]llmStatus, len(llmProviders))
	onCount, onIdx := 0, -1
	for i, p := range llmProviders {
		source, _ := resolveLLM(p, "")
		on, _ := s.store.Connected("llm:" + p)
		sts[i] = llmStatus{Name: p, Connected: on && source != "", Source: source, Default: def == p,
			Model: models[p], Effort: efforts[p]}
		if sts[i].Connected {
			onCount++
			onIdx = i
			if budget, clamped := chatBudget(p); clamped {
				sts[i].BudgetNote = fmt.Sprintf("token_budget %s exceeds %s's window — chat clamped to %s",
					fmtTok(int64(cfg.TokenBudget)), chatModel(p), fmtTok(int64(budget)))
			}
		}
	}
	if def == "" && onCount == 1 {
		sts[onIdx].Default = true
	}
	return sts
}

// ConnectLLM resolves the provider's credentials, live-validates api keys and
// persists the connected state. No poller to start: providers are
// request/response, so a server restart needs no resume work.
func (s *Server) ConnectLLM(ctx context.Context, provider, reqKey string) (llmStatus, int, error) {
	source, key := resolveLLM(provider, reqKey)
	if source == "" {
		return llmStatus{}, http.StatusBadRequest, errKeyRequired
	}
	if source == "api_key" {
		if err := validateLLMKey(ctx, provider, key); err != nil {
			return llmStatus{}, http.StatusUnauthorized, err
		}
	}
	// ponytail: llm rows share the channels table as "llm:<name>" — same
	// (name, connected) shape; split into an own table if the schemas diverge.
	if err := s.store.SetConnected("llm:"+provider, true); err != nil {
		return llmStatus{}, http.StatusInternalServerError, err
	}
	st := llmStatus{Name: provider, Connected: true, Source: source}
	for _, ls := range s.llmStatuses() {
		if ls.Name == provider {
			st.Default = ls.Default
		}
	}
	return st, http.StatusOK, nil
}
