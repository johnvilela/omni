package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"
)

type button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// tgReply is one outgoing message: text plus an optional inline keyboard.
// Edit and StripKeyboard act only on button-tap replies (the poller knows
// which message was tapped); plain sends ignore them.
type tgReply struct {
	Text          string
	Keyboard      [][]button
	Edit          bool // replace the tapped message (text + keyboard) instead of sending a new one
	StripKeyboard bool // remove the tapped message's now-stale buttons, then send Text as usual
	DeleteInbound bool // delete the message that triggered this reply (e.g. a sudo password)
}

// tgFile is one inbound photo/document's metadata — no bytes; the download
// happens after the pairing gate, one layer up.
type tgFile struct {
	ID      string // telegram file_id
	Name    string // document file_name; empty for photos
	Caption string
}

// Telegram talks to the Telegram Bot API for one bot token.
type Telegram struct {
	base     string
	apiBase  string // kept for downloads: apiBase + "/file/bot" + token
	token    string
	client   *http.Client
	answer   func(ctx context.Context, fromID int64, text string) tgReply // reply to one text message
	callback func(ctx context.Context, fromID int64, data string) tgReply // reply to one button tap
	file     func(ctx context.Context, fromID int64, f tgFile) tgReply    // reply to one photo/document message
}

func NewTelegram(apiBase, token string) *Telegram {
	return &Telegram{
		base:    apiBase + "/bot" + token,
		apiBase: apiBase,
		token:   token,
		// 50s long poll + slack; individual calls carry ctx too
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
		Photo   []struct {
			FileID string `json:"file_id"`
		} `json:"photo"`
		Document *struct {
			FileID   string `json:"file_id"`
			FileName string `json:"file_name"`
		} `json:"document"`
	} `json:"message"`
	CallbackQuery *struct {
		ID   string `json:"id"`
		From struct {
			ID int64 `json:"id"`
		} `json:"from"`
		Message *struct {
			MessageID int64 `json:"message_id"`
			Chat      struct {
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
		{"command": "agent", "description": "agent session with tools — /agent [@openai|@claude] task"},
		{"command": "task", "description": "long checkpointed task — /task <goal> | /task #id <text>"},
		{"command": "tasks", "description": "list long tasks — pause, resume, cancel"},
		{"command": "sessions", "description": "list recent sessions — tap to resume"},
		{"command": "usage", "description": "llm usage per provider — tokens, cost, limits"},
		{"command": "context", "description": "context usage of the active session — tokens per source"},
		{"command": "crons", "description": "list scheduled jobs — tap to delete"},
		{"command": "pin", "description": "toggle pinned status dashboard — /pin [full|clean]"},
		{"command": "terminal", "description": "shell mode — run commands on this PC as you (/exit to leave)"},
		{"command": "interrupt", "description": "^C the running command — /terminal or $ one-shot"},
	}}, nil)
}

// sendReturningID sends one plain message and returns its message id (the
// chunking send discards it; the pin dashboard needs it to edit later).
func (t *Telegram) sendReturningID(ctx context.Context, chatID int64, text string) (int64, error) {
	var res struct {
		MessageID int64 `json:"message_id"`
	}
	err := t.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, &res)
	return res.MessageID, err
}

// editMessage rewrites one message's text and keyboard in place; nil kb drops
// any existing buttons (telegram removes markup absent from the edit).
func (t *Telegram) editMessage(ctx context.Context, chatID, msgID int64, text string, kb [][]button) error {
	body := map[string]any{"chat_id": chatID, "message_id": msgID, "text": text}
	if kb != nil {
		body["reply_markup"] = map[string]any{"inline_keyboard": kb}
	}
	return t.call(ctx, "editMessageText", body, nil)
}

// editMarkup replaces one message's inline keyboard only; nil strips it.
func (t *Telegram) editMarkup(ctx context.Context, chatID, msgID int64, kb [][]button) error {
	if kb == nil {
		kb = [][]button{}
	}
	return t.call(ctx, "editMessageReplyMarkup", map[string]any{"chat_id": chatID,
		"message_id": msgID, "reply_markup": map[string]any{"inline_keyboard": kb}}, nil)
}

func (t *Telegram) pinMessage(ctx context.Context, chatID, msgID int64) error {
	return t.call(ctx, "pinChatMessage", map[string]any{"chat_id": chatID, "message_id": msgID, "disable_notification": true}, nil)
}

func (t *Telegram) unpinMessage(ctx context.Context, chatID, msgID int64) error {
	return t.call(ctx, "unpinChatMessage", map[string]any{"chat_id": chatID, "message_id": msgID}, nil)
}

func (t *Telegram) deleteMessage(ctx context.Context, chatID, msgID int64) error {
	return t.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": msgID}, nil)
}

// downloadFile resolves a file_id via getFile and streams the bytes to dest.
// Telegram caps getFile at 20MB — bigger files just surface its error.
func (t *Telegram) downloadFile(ctx context.Context, fileID, dest string) error {
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := t.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.apiBase+"/file/bot"+t.token+"/"+f.FilePath, nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram file download: %s", resp.Status)
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		out.Close()
		os.Remove(dest) // no partial files in the inbox
		return err
	}
	return out.Close()
}

// upload sends one local file via multipart; method/field are
// sendPhoto/photo or sendDocument/document.
func (t *Telegram) upload(ctx context.Context, method, field string, chatID int64, path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	// ponytail: whole file buffered (telegram caps uploads at 50MB); io.Pipe if it hurts
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	part, err := w.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, in); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/"+method, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
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
	return nil
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
// Returns when ctx is cancelled. t.answer stays synchronous but fast: llm
// work is dispatched to background workers one layer up (server/queue.go),
// so the loop never blocks on an answer.
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
				r := t.callback(ctx, q.From.ID, q.Data)
				if r.Edit && r.Text != "" {
					// refresh the tapped message in place (e.g. /crons after a delete)
					if err := t.editMessage(ctx, q.Message.Chat.ID, q.Message.MessageID, r.Text, r.Keyboard); err != nil {
						log.Printf("telegram: editMessageText: %v", err)
					}
					continue
				}
				if r.StripKeyboard {
					t.editMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil) // best-effort
				}
				t.send(ctx, q.Message.Chat.ID, r)
				continue
			}
			if m := u.Message; m != nil && (len(m.Photo) > 0 || m.Document != nil) {
				if t.file == nil {
					continue
				}
				f := tgFile{Caption: m.Caption}
				if m.Document != nil {
					f.ID, f.Name = m.Document.FileID, m.Document.FileName
				} else {
					f.ID = m.Photo[len(m.Photo)-1].FileID // last PhotoSize = largest
				}
				from := m.From.ID
				if from == 0 {
					from = m.Chat.ID
				}
				stop := t.typing(ctx, m.Chat.ID)
				reply := t.file(ctx, from, f)
				stop()
				t.send(ctx, m.Chat.ID, reply)
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
			if reply.DeleteInbound {
				t.deleteMessage(ctx, u.Message.Chat.ID, u.Message.MessageID) // best-effort: keep the sudo password out of history
			}
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
