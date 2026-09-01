package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"strconv"
)

// Pairing is one chat user's pairing state for a channel.
type Pairing struct {
	UserID   string `json:"user_id"`
	Code     string `json:"code"`
	Approved bool   `json:"approved"`
}

// codeAlphabet avoids ambiguous characters (0/O, 1/I/L).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newPairingCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// gatedAnswer lets only paired senders through to the llm answer flow.
// An unknown sender gets the full pairing instructions once; after that a
// short reminder until the owner approves the code.
func (s *Server) gatedAnswer(ctx context.Context, fromID int64, text string) string {
	id := strconv.FormatInt(fromID, 10)
	p, ok, err := s.store.Pairing("telegram", id)
	if err != nil {
		log.Printf("pairing: %v", err)
		return "⚠ internal error, try again"
	}
	if ok && p.Approved {
		return s.answerNotice(ctx, text)
	}
	if !ok {
		code := newPairingCode()
		if err := s.store.AddPairing("telegram", id, code); err != nil {
			log.Printf("pairing: %v", err)
			return "⚠ internal error, try again"
		}
		return fmt.Sprintf(`%s: access not configured.

Your Telegram user id: %s
Pairing code:

%s

Ask the bot owner to approve with:

%s pairing approve telegram %s`, app, id, code, app, code)
	}
	return "⚠ not authorized — pairing " + p.Code + " awaiting approval"
}
