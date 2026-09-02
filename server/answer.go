package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// answerHTTP allows real generation time; llmHTTP's 15s is only for key checks.
var answerHTTP = &http.Client{Timeout: 2 * time.Minute}

// llmModels are the hardcoded cheap defaults for api_key calls; vendor CLIs
// use their own defaults.
var llmModels = map[string]string{
	"openai": "gpt-4o-mini",
	"claude": "claude-3-5-haiku-latest",
	"gemini": "gemini-2.0-flash",
}

// configuredModel is the model picked via `omni llm model`, "" if unset.
func configuredModel(provider string) string {
	cfg := readConfig()
	return map[string]string{"openai": cfg.OpenAIModel, "claude": cfg.ClaudeModel, "gemini": cfg.GeminiModel}[provider]
}

// Answer asks the default llm one single-turn question: text in, text out,
// no history. The chat flow, session naming and the memory digest all build
// on it; ChatAnswer is the history-aware entry point.
func (s *Server) Answer(ctx context.Context, text string) (string, error) {
	var def *llmStatus
	for _, ls := range s.llmStatuses() {
		if ls.Default {
			def = &ls
			break
		}
	}
	if def == nil {
		return "", fmt.Errorf("no default llm — run: omni llm connect")
	}
	if !def.Connected {
		return "", fmt.Errorf("%s is not connected — run: omni llm connect -p %s", def.Name, def.Name)
	}

	source, key := resolveLLM(def.Name, "")
	var reply string
	var err error
	switch source {
	case "api_key":
		reply, err = askAPI(ctx, def.Name, key, text)
	case "oauth":
		cli := cliArgs(def.Name, text)
		reply, err = runCLI(ctx, "", 2*time.Minute, cli[0], cli[1:]...)
	case "claude-code":
		cli := cliArgs("claude", text)
		reply, err = runCLI(ctx, "", 2*time.Minute, cli[0], cli[1:]...)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(reply), nil
}

// answerNotice is the telegram poller's entry point: history-aware, errors
// become a visible notice instead of silence.
func (s *Server) answerNotice(ctx context.Context, text string) string {
	reply, err := s.ChatAnswer(ctx, text)
	if err != nil {
		return "⚠ " + err.Error()
	}
	if reply == "" {
		return "(empty reply)" // telegram rejects empty text
	}
	return reply
}

// cliArgs builds one vendor CLI invocation. Every CLI runs bare — no MCP
// servers, no user config/settings/hooks, tools disabled or read-only: the
// host's agent setup must never leak into chat answers, and omni will inject
// its own tools later.
func cliArgs(provider, text string) []string {
	model := configuredModel(provider)
	switch provider {
	case "openai":
		// codex's shell tool can't be disabled; the read-only sandbox
		// confines it. --ignore-user-config drops MCP servers + hooks but
		// keeps auth.
		args := []string{"codex", "exec", "--skip-git-repo-check", "--ignore-user-config", "-s", "read-only"}
		if model != "" {
			args = append(args, "-m", model) // before the positional prompt
		}
		return append(args, text)
	case "claude":
		// prompt before --tools: it's variadic and would swallow a trailing
		// positional. --setting-sources "" ignores user hooks/settings.
		args := []string{"claude", "-p", text, "--tools", "", "--strict-mcp-config", "--setting-sources", ""}
		if model != "" {
			args = append(args, "--model", model)
		}
		return args
	case "gemini":
		// the MCP allowlist rejects empty names, so allow one that can't
		// exist — same effect as none. ponytail: gemini's built-in read
		// tools have no disable flag; mutating tools are auto-denied in
		// non-interactive mode. Use the policy engine if that ever needs
		// tightening.
		args := []string{"gemini", "--allowed-mcp-server-names", "omni-none", "-e", "none"}
		if model != "" {
			args = append(args, "-m", model)
		}
		return append(args, "-p", text)
	}
	return nil
}

// runCLI shells out to a vendor CLI (codex/claude/gemini) whose stored login
// omni can't use directly. dir "" inherits the server's cwd. ponytail: stdout
// taken as-is; refine flags per CLI if the output turns out noisy.
func runCLI(ctx context.Context, dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		// stderr can echo the whole composed prompt (codex does — chat
		// history included), and this error reaches the chat as the reply.
		// Full dump to the server log; only the last line to the user.
		stderr := strings.TrimSpace(errb.String())
		if stderr == "" {
			return "", fmt.Errorf("%s: %v", name, err)
		}
		log.Printf("%s failed: %v\n%s", name, err, stderr)
		if i := strings.LastIndexByte(stderr, '\n'); i >= 0 {
			stderr = strings.TrimSpace(stderr[i+1:])
		}
		return "", fmt.Errorf("%s: %v: %s", name, err, stderr)
	}
	return out.String(), nil
}

// askAPI does one plain text-in/text-out call against a provider's HTTP API.
func askAPI(ctx context.Context, provider, key, text string) (string, error) {
	base := llmAPIBase(provider)
	model := configuredModel(provider)
	if model == "" {
		model = llmModels[provider]
	}
	var req *http.Request
	var err error
	parse := func(*json.Decoder) (string, error) { return "", nil }

	switch provider {
	case "openai":
		req, err = jsonReq(ctx, base+"/v1/chat/completions", map[string]any{
			"model":    model,
			"messages": []map[string]string{{"role": "user", "content": text}},
		})
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		parse = func(d *json.Decoder) (string, error) {
			var r struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if err := d.Decode(&r); err != nil || len(r.Choices) == 0 {
				return "", fmt.Errorf("openai: bad response: %v", err)
			}
			return r.Choices[0].Message.Content, nil
		}
	case "claude":
		req, err = jsonReq(ctx, base+"/v1/messages", map[string]any{
			"model":      model,
			"max_tokens": 1024,
			"messages":   []map[string]string{{"role": "user", "content": text}},
		})
		if err == nil {
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		parse = func(d *json.Decoder) (string, error) {
			var r struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := d.Decode(&r); err != nil || len(r.Content) == 0 {
				return "", fmt.Errorf("claude: bad response: %v", err)
			}
			return r.Content[0].Text, nil
		}
	case "gemini":
		req, err = jsonReq(ctx, base+"/v1beta/models/"+model+":generateContent?key="+url.QueryEscape(key), map[string]any{
			"contents": []map[string]any{{"parts": []map[string]string{{"text": text}}}},
		})
		parse = func(d *json.Decoder) (string, error) {
			var r struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
			}
			if err := d.Decode(&r); err != nil || len(r.Candidates) == 0 || len(r.Candidates[0].Content.Parts) == 0 {
				return "", fmt.Errorf("gemini: bad response: %v", err)
			}
			return r.Candidates[0].Content.Parts[0].Text, nil
		}
	}
	if err != nil {
		return "", err
	}
	resp, err := answerHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", provider, resp.Status)
	}
	return parse(json.NewDecoder(resp.Body))
}

func jsonReq(ctx context.Context, url string, body any) (*http.Request, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}
