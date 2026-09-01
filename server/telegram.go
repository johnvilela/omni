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
	answer func(ctx context.Context, text string) string // reply to one text message
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

// typing keeps the "typing…" indicator alive while an answer is produced;
// telegram shows it for ~5s per sendChatAction, so re-send until stopped.
func (t *Telegram) typing(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		for {
			t.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"}, nil) // best-effort
			select {
			case <-ctx.Done():
				return
			case <-time.After(4 * time.Second):
			}
		}
	}()
	return cancel
}

// Poll long-polls getUpdates and replies to each text message via t.answer.
// Returns when ctx is cancelled.
// ponytail: answers serially in the poll loop; goroutine-per-message if chat
// volume ever matters.
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
			stop := t.typing(ctx, u.Message.Chat.ID)
			reply := t.answer(ctx, u.Message.Text)
			stop()
			err := t.call(ctx, "sendMessage", map[string]any{
				"chat_id": u.Message.Chat.ID,
				"text":    reply,
			}, nil)
			if err != nil {
				log.Printf("telegram: sendMessage: %v", err)
			}
		}
	}
}
