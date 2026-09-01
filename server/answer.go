package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Answer asks the default llm to answer text. Single-turn, no history.
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
		cli := map[string][]string{
			"openai": {"codex", "exec", "--skip-git-repo-check", text},
			"claude": {"claude", "-p", text},
			"gemini": {"gemini", "-p", text},
		}[def.Name]
		reply, err = runCLI(ctx, cli[0], cli[1:]...)
	case "claude-code":
		reply, err = runCLI(ctx, "claude", "-p", text)
	}
	if err != nil {
		return "", err
	}
	if reply = strings.TrimSpace(reply); reply == "" {
		reply = "(empty reply)" // telegram rejects empty text
	}
	return reply, nil
}

// answerNotice is Answer for the telegram poller: errors become a visible
// notice instead of silence.
func (s *Server) answerNotice(ctx context.Context, text string) string {
	reply, err := s.Answer(ctx, text)
	if err != nil {
		return "⚠ " + err.Error()
	}
	return reply
}

// runCLI shells out to a vendor CLI (codex/claude/gemini) whose stored login
// omni can't use directly. ponytail: stdout taken as-is; refine flags per CLI
// if the output turns out noisy.
func runCLI(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// askAPI does one plain text-in/text-out call against a provider's HTTP API.
func askAPI(ctx context.Context, provider, key, text string) (string, error) {
	base := llmAPIBase(provider)
	model := llmModels[provider]
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
