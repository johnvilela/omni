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
	"time"
)

// llmStatus is the wire status of one LLM provider.
type llmStatus struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Source    string `json:"source,omitempty"` // oauth | api_key | claude-code
	Default   bool   `json:"default,omitempty"`
}

var llmProviders = []string{"openai", "claude", "gemini"}

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

// validateLLMKey checks an api key with the cheapest authenticated call each
// provider has: listing models.
func validateLLMKey(ctx context.Context, provider, key string) error {
	base := llmAPIBase(provider)
	var req *http.Request
	var err error
	switch provider {
	case "openai":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	case "claude":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models", nil)
		if err == nil {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case "gemini":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1beta/models?key="+url.QueryEscape(key), nil)
	}
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

// llmStatuses reports every provider: connected means the user connected it
// AND its credentials still resolve; source is what would be used right now.
// With no default_llm configured and exactly one provider connected, that one
// is the implied default.
func (s *Server) llmStatuses() []llmStatus {
	def := readConfig().DefaultLLM
	sts := make([]llmStatus, len(llmProviders))
	onCount, onIdx := 0, -1
	for i, p := range llmProviders {
		source, _ := resolveLLM(p, "")
		on, _ := s.store.Connected("llm:" + p)
		sts[i] = llmStatus{Name: p, Connected: on && source != "", Source: source, Default: def == p}
		if sts[i].Connected {
			onCount++
			onIdx = i
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
