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

// ErrTokenRequired means the server has no bot token; prompt the user.
var ErrTokenRequired = errors.New("token_required")

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
