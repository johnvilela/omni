package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
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
	return s.answerWith(ctx, "", text)
}

// answerWith is Answer with a provider override; "" means the default llm
// (chat sessions pinned via a sticky @pick pass their own).
func (s *Server) answerWith(ctx context.Context, provider, text string) (string, error) {
	var def *llmStatus
	for _, ls := range s.llmStatuses() {
		if (provider == "" && ls.Default) || ls.Name == provider {
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
	var u callUsage
	var err error
	switch source {
	case "api_key":
		reply, u, err = askAPI(ctx, def.Name, key, text)
	case "oauth":
		reply, u, err = runChatCLI(ctx, def.Name, text)
	case "claude-code":
		reply, u, err = runChatCLI(ctx, "claude", text)
	}
	if err != nil {
		return "", err
	}
	s.recordUsage(def.Name, u)
	return strings.TrimSpace(reply), nil
}

// callUsage is what one llm call consumed; cost only when the provider
// reported one. ctx is the context size of the last request in the call —
// what /context shows — set only by the vendor CLI paths.
type callUsage struct {
	in, out, ctx int64
	cost         float64
}

// recordUsage best-effort logs one call's consumption for /usage.
func (s *Server) recordUsage(provider string, u callUsage) {
	if u.in+u.out == 0 {
		return // path with no usage data (gemini CLI)
	}
	if err := s.store.AddUsage(provider, u.in, u.out, u.cost, time.Now().Unix()); err != nil {
		log.Printf("usage: %v", err)
	}
}

// runChatCLI runs one bare chat turn on a vendor CLI, asking claude and codex
// for structured output so token usage can be recorded. gemini has no
// usage-bearing non-interactive output — its CLI calls go untracked.
func runChatCLI(ctx context.Context, provider, text string) (string, callUsage, error) {
	args := cliArgs(provider, text)
	switch provider {
	case "claude":
		out, err := runCLI(ctx, "", 2*time.Minute, args[0], append(args[1:], "--output-format", "json")...)
		if err != nil {
			return "", callUsage{}, err
		}
		res, err := parseClaudeJSON(out)
		if err != nil {
			return "", callUsage{}, err
		}
		return res.Result, res.usage(), nil
	case "openai":
		tmp, err := os.CreateTemp("", "omni-chat-*")
		if err != nil {
			return "", callUsage{}, err
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		// flags must precede the positional prompt (last element of args)
		full := append(args[:len(args)-1:len(args)-1], "--json", "-o", tmp.Name(), args[len(args)-1])
		out, err := runCLI(ctx, "", 2*time.Minute, full[0], full[1:]...)
		if err != nil {
			return "", callUsage{}, err
		}
		_, u := parseCodexEvents(out)
		raw, err := os.ReadFile(tmp.Name())
		if err != nil {
			return "", callUsage{}, err
		}
		return string(raw), u, nil
	}
	out, err := runCLI(ctx, "", 2*time.Minute, args[0], args[1:]...)
	return out, callUsage{}, err
}

// answerNotice is the chat worker's entry point: history-aware, errors
// become a visible notice instead of silence.
func (s *Server) answerNotice(ctx context.Context, sess Session, text string) string {
	reply, err := s.chatAnswer(ctx, sess, text)
	if err != nil {
		return "⚠ " + err.Error()
	}
	return reply // "" = approval proposal sent out-of-band, nothing to deliver
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

// askAPI does one plain text-in/text-out call against a provider's HTTP API,
// also returning the token usage the response reports.
func askAPI(ctx context.Context, provider, key, text string) (string, callUsage, error) {
	base := llmAPIBase(provider)
	model := configuredModel(provider)
	if model == "" {
		model = llmModels[provider]
	}
	var req *http.Request
	var err error
	var u callUsage
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
				Usage struct {
					In  int64 `json:"prompt_tokens"`
					Out int64 `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := d.Decode(&r); err != nil || len(r.Choices) == 0 {
				return "", fmt.Errorf("openai: bad response: %v", err)
			}
			u = callUsage{in: r.Usage.In, out: r.Usage.Out}
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
				Usage struct {
					In  int64 `json:"input_tokens"`
					Out int64 `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := d.Decode(&r); err != nil || len(r.Content) == 0 {
				return "", fmt.Errorf("claude: bad response: %v", err)
			}
			u = callUsage{in: r.Usage.In, out: r.Usage.Out}
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
				Usage struct {
					In  int64 `json:"promptTokenCount"`
					Out int64 `json:"candidatesTokenCount"`
				} `json:"usageMetadata"`
			}
			if err := d.Decode(&r); err != nil || len(r.Candidates) == 0 || len(r.Candidates[0].Content.Parts) == 0 {
				return "", fmt.Errorf("gemini: bad response: %v", err)
			}
			u = callUsage{in: r.Usage.In, out: r.Usage.Out}
			return r.Candidates[0].Content.Parts[0].Text, nil
		}
	}
	if err != nil {
		return "", callUsage{}, err
	}
	resp, err := answerHTTP.Do(req)
	if err != nil {
		return "", callUsage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", callUsage{}, fmt.Errorf("%s: %s", provider, resp.Status)
	}
	reply, err := parse(json.NewDecoder(resp.Body))
	return reply, u, err
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
