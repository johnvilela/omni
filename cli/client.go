package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Channel mirrors the server's channel status JSON.
type Channel struct {
	Name        string `json:"name"`
	Connected   bool   `json:"connected"`
	BotUsername string `json:"bot_username"`
}

// LLM mirrors the server's llm provider status JSON.
type LLM struct {
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Source    string `json:"source"`
	Default   bool   `json:"default"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	BudgetNote string `json:"budget_note"`
}

// ErrTokenRequired means the server has no bot token; prompt the user.
var ErrTokenRequired = errors.New("token_required")

// ErrKeyRequired means the server has no credentials for the provider; prompt.
var ErrKeyRequired = errors.New("key_required")

// Client is a tiny HTTP client for the omni server API.
type Client struct {
	Base string
	http *http.Client
}

func NewClient(base string) *Client {
	// connect validates the token against Telegram, so allow it time
	return &Client{Base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, c.Base+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "token_required" {
			return ErrTokenRequired
		}
		if e.Error == "key_required" {
			return ErrKeyRequired
		}
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("server: %s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Channels() ([]Channel, error) {
	var chs []Channel
	err := c.do(http.MethodGet, "/channels", nil, &chs)
	return chs, err
}

func (c *Client) Channel(name string) (Channel, error) {
	var ch Channel
	err := c.do(http.MethodGet, "/channels/"+name, nil, &ch)
	return ch, err
}

func (c *Client) Connect(name, token string) (Channel, error) {
	var ch Channel
	err := c.do(http.MethodPost, "/channels/"+name+"/connect", map[string]string{"token": token}, &ch)
	return ch, err
}

// Pairing mirrors the server's pairing JSON.
type Pairing struct {
	UserID   string `json:"user_id"`
	Code     string `json:"code"`
	Approved bool   `json:"approved"`
}

func (c *Client) Pairings(channel string) ([]Pairing, error) {
	var ps []Pairing
	err := c.do(http.MethodGet, "/pairing/"+channel, nil, &ps)
	return ps, err
}

func (c *Client) ApprovePairing(channel, code string) (Pairing, error) {
	var p Pairing
	err := c.do(http.MethodPost, "/pairing/"+channel+"/approve", map[string]string{"code": code}, &p)
	return p, err
}

func (c *Client) RevokePairing(channel, userID string) error {
	return c.do(http.MethodPost, "/pairing/"+channel+"/revoke", map[string]string{"user_id": userID}, nil)
}

func (c *Client) LLMs() ([]LLM, error) {
	var ls []LLM
	err := c.do(http.MethodGet, "/llm", nil, &ls)
	return ls, err
}

func (c *Client) LLM(name string) (LLM, error) {
	var l LLM
	err := c.do(http.MethodGet, "/llm/"+name, nil, &l)
	return l, err
}

func (c *Client) LLMModels(name string) ([]string, error) {
	var ms []string
	err := c.do(http.MethodGet, "/llm/"+name+"/models", nil, &ms)
	return ms, err
}

// SyncPlugins tells a running server to re-publish the telegram command menu
// after a plugin install/remove; callers treat failure as best-effort.
func (c *Client) SyncPlugins() error {
	return c.do(http.MethodPost, "/plugins/sync", nil, nil)
}

func (c *Client) ConnectLLM(name, key string) (LLM, error) {
	var l LLM
	err := c.do(http.MethodPost, "/llm/"+name+"/connect", map[string]string{"key": key}, &l)
	return l, err
}
