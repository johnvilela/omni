package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	"unicode/utf8"
)

type button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// tgReply is one outgoing message: text plus an optional inline keyboard.
type tgReply struct {
	Text     string
	Keyboard [][]button
}

// Telegram talks to the Telegram Bot API for one bot token.
type Telegram struct {
	base     string
	client   *http.Client
	answer   func(ctx context.Context, fromID int64, text string) tgReply // reply to one text message
	callback func(ctx context.Context, fromID int64, data string) tgReply // reply to one button tap
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
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Text string `json:"text"`
	} `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
		Data string `json:"data"`
	} `json:"callback_query"`
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

// registerCommands publishes the "/" autocomplete menu clients show when the
// user types a slash. Idempotent — reconnecting just re-publishes.
func (t *Telegram) registerCommands(ctx context.Context) error {
	return t.call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{
		{"command": "new", "description": "start a fresh chat session"},
		{"command": "agent", "description": "start an agent session (tools + browser)"},
		{"command": "sessions", "description": "list recent sessions — tap to resume"},
	}}, nil)
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
// ponytail: answers serially in the poll loop — an agent turn can hold it for
// up to 15 min (messages queue in getUpdates, none lost); answer in a
// goroutine through t.send if that ever hurts.
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
			if q := u.CallbackQuery; q != nil {
				// best-effort: stops the client's button spinner
				t.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID}, nil)
				if t.callback == nil || q.Message == nil {
					continue // taps on messages too old for telegram to echo back
				}
				t.send(ctx, q.Message.Chat.ID, t.callback(ctx, q.From.ID, q.Data))
				continue
			}
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			from := u.Message.From.ID
			if from == 0 {
				from = u.Message.Chat.ID // channel posts carry no sender
			}
			stop := t.typing(ctx, u.Message.Chat.ID)
			reply := t.answer(ctx, from, u.Message.Text)
			stop()
			t.send(ctx, u.Message.Chat.ID, reply)
		}
	}
}

// send delivers one reply, chunked to telegram's 4096-char message cap; the
// keyboard rides the last chunk. Empty text means silence (e.g. a
// rate-limited unpaired sender) — telegram rejects empty messages anyway.
func (t *Telegram) send(ctx context.Context, chatID int64, r tgReply) {
	if r.Text == "" {
		return
	}
	parts := chunks(r.Text, 4096)
	for i, p := range parts {
		body := map[string]any{"chat_id": chatID, "text": p}
		if i == len(parts)-1 && r.Keyboard != nil {
			body["reply_markup"] = map[string]any{"inline_keyboard": r.Keyboard}
		}
		if err := t.call(ctx, "sendMessage", body, nil); err != nil {
			log.Printf("telegram: sendMessage: %v", err)
		}
	}
}

// chunks splits s into pieces of at most max bytes, cutting only at rune
// boundaries (byte length ≥ UTF-16 length, so max bytes never exceeds
// telegram's max characters).
func chunks(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return append(out, s)
}
