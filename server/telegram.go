package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Telegram talks to the Telegram Bot API for one bot token.
type Telegram struct {
	base   string
	client *http.Client
}

func NewTelegram(apiBase, token string) *Telegram {
	return &Telegram{
		base: apiBase + "/bot" + token,
		// 50s long poll + slack; individual calls carry ctx too
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (t *Telegram) call(ctx context.Context, method string, body any, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/"+method, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var api apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&api); err != nil {
		return err
	}
	if !api.OK {
		return fmt.Errorf("telegram %s: %s", method, api.Description)
	}
	if out != nil {
		return json.Unmarshal(api.Result, out)
	}
	return nil
}

func (t *Telegram) GetMe(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := t.call(ctx, "getMe", nil, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// Poll long-polls getUpdates and replies to each text message with it reversed.
// Returns when ctx is cancelled.
func (t *Telegram) Poll(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		var updates []update
		err := t.call(ctx, fmt.Sprintf("getUpdates?timeout=50&offset=%d", offset), nil, &updates)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("telegram: getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			err := t.call(ctx, "sendMessage", map[string]any{
				"chat_id": u.Message.Chat.ID,
				"text":    Reverse(u.Message.Text),
			}, nil)
			if err != nil {
				log.Printf("telegram: sendMessage: %v", err)
			}
		}
	}
}
