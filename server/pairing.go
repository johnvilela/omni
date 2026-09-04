package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// Pairing is one chat user's pairing state for a channel.
type Pairing struct {
	UserID   string `json:"user_id"`
	Code     string `json:"code"`
	Approved bool   `json:"approved"`
}

const (
	pairReplyLimit     = 3                // unpaired replies per sender per window
	pairReplyWindow    = 10 * time.Minute // then silence until the window passes
	maxPendingPairings = 50               // cap on rows a many-account flood can create
)

type pairHit struct {
	count int
	since time.Time
}

// allowPairReply counts this unpaired sender's attempt and reports whether
// they may still get a reply. In-memory: a restart resets it, which is fine
// for abuse throttling.
func (s *Server) allowPairReply(fromID int64, now time.Time) bool {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if s.pairHits == nil {
		s.pairHits = map[int64]*pairHit{}
	}
	// ponytail: lazy sweep bounds the map under many-account floods
	if len(s.pairHits) >= 1000 {
		for k, h := range s.pairHits {
			if now.Sub(h.since) >= pairReplyWindow {
				delete(s.pairHits, k)
			}
		}
	}
	h := s.pairHits[fromID]
	if h == nil || now.Sub(h.since) >= pairReplyWindow {
		s.pairHits[fromID] = &pairHit{count: 1, since: now}
		return true
	}
	h.count++
	return h.count <= pairReplyLimit
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

// gatedAnswer lets only paired senders through to the command/answer flow.
func (s *Server) gatedAnswer(ctx context.Context, fromID int64, text string) tgReply {
	if ok, r := s.gate(fromID); !ok {
		return r
	}
	return s.handleMessage(ctx, text)
}

// gate checks one sender's pairing; !ok means the reply carries the pairing
// flow (or silence when rate-limited). An unknown sender gets the full
// pairing instructions once; after that a short reminder until the owner
// approves the code.
func (s *Server) gate(fromID int64) (bool, tgReply) {
	id := strconv.FormatInt(fromID, 10)
	p, ok, err := s.store.Pairing("telegram", id)
	if err != nil {
		log.Printf("pairing: %v", err)
		return false, tgReply{Text: "⚠ internal error, try again"}
	}
	if ok && p.Approved {
		return true, tgReply{}
	}
	if !s.allowPairReply(fromID, time.Now()) {
		return false, tgReply{} // rate-limited: the poller sends nothing
	}
	if !ok {
		if n, err := s.store.PendingPairings("telegram"); err != nil {
			log.Printf("pairing: %v", err)
			return false, tgReply{Text: "⚠ internal error, try again"}
		} else if n >= maxPendingPairings {
			return false, tgReply{Text: "⚠ pairing is busy — try again later"}
		}
		code := newPairingCode()
		if err := s.store.AddPairing("telegram", id, code); err != nil {
			log.Printf("pairing: %v", err)
			return false, tgReply{Text: "⚠ internal error, try again"}
		}
		return false, tgReply{Text: fmt.Sprintf(`%s: access not configured.

Your Telegram user id: %s
Pairing code:

%s

Ask the bot owner to approve with:

%s pairing approve telegram %s`, app, id, code, app, code)}
	}
	return false, tgReply{Text: "⚠ not authorized — pairing " + p.Code + " awaiting approval"}
}

// gatedCallback lets only approved senders act on button taps; anyone else
// gets silence (their spinner was already answered). Data is a session uuid
// (resume), a "cron-del:<id>" (delete from /crons), an approval-gate action
// "appr:/alws:/deny:/edit:<id>" (server/approval.go), or a one-tap-update
// action "upd:/updlog:/updign:<tag>" (server/update.go).
func (s *Server) gatedCallback(_ context.Context, fromID int64, data string) tgReply {
	p, ok, err := s.store.Pairing("telegram", strconv.FormatInt(fromID, 10))
	if err != nil || !ok || !p.Approved {
		return tgReply{}
	}
	if id, ok := strings.CutPrefix(data, "cron-del:"); ok {
		return s.deleteCronCallback(id)
	}
	if id, ok := strings.CutPrefix(data, "task-pause:"); ok {
		return s.taskCallback("pause", id)
	}
	if id, ok := strings.CutPrefix(data, "task-resume:"); ok {
		return s.taskCallback("resume", id)
	}
	if id, ok := strings.CutPrefix(data, "task-cancel:"); ok {
		return s.taskCallback("cancel", id)
	}
	if id, ok := strings.CutPrefix(data, "appr:"); ok {
		return s.approveProposal(id, false)
	}
	if id, ok := strings.CutPrefix(data, "alws:"); ok {
		return s.approveProposal(id, true)
	}
	if id, ok := strings.CutPrefix(data, "deny:"); ok {
		return s.denyProposal(id)
	}
	if id, ok := strings.CutPrefix(data, "edit:"); ok {
		return s.editProposal(id)
	}
	if tag, ok := strings.CutPrefix(data, "upd:"); ok {
		return s.startUpdate(tag)
	}
	if tag, ok := strings.CutPrefix(data, "updlog:"); ok {
		return s.updateChangelog(tag)
	}
	if tag, ok := strings.CutPrefix(data, "updign:"); ok {
		return s.ignoreUpdate(tag)
	}
	return s.resumeSession(data)
}
