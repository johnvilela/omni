package main

import (
	"context"
	"log"
	"slices"
	"time"
)

// deleteWindow is how far back telegram lets a bot delete messages.
const deleteWindow = 48 * time.Hour

// trackMessage is the telegram seen hook: every message id sent or received
// lands in tg_messages so clearChats can delete it later.
func (s *Server) trackMessage(chatID, msgID int64) {
	if err := s.store.AddTgMessage(chatID, msgID, time.Now().Unix()); err != nil {
		log.Printf("clear: track chat %d: %v", chatID, err)
	}
}

// clearChats deletes every tracked message in every approved chat so the
// telegram view looks clean again — the pinned dashboard stays. Visual only:
// sessions, messages and memory are untouched. Best-effort and silent:
// telegram refuses deletes older than 48h (those ids are skipped up front)
// and messages sent before tracking existed were never recorded. Tracked rows
// up to the newest id read are dropped either way — a message that can't be
// deleted now never can be — while anything tracked mid-clear survives.
//
// Runs synchronously in the poller before the command's reply is sent, so a
// confirmation (or a resumed session's unread answer) is the first message
// of the clean view. Callers clear before enqueueing new work for the same
// reason: an answer pushed mid-clear could otherwise be deleted.
func (s *Server) clearChats(ctx context.Context) {
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return
	}
	keep := map[int64]int64{} // chat → pinned dashboard message id
	if pins, err := s.store.Pins(); err == nil {
		for _, p := range pins {
			keep[p.ChatID] = p.MessageID
		}
	}
	cutoff := time.Now().Add(-deleteWindow).Unix()
	for _, chat := range s.ownerChats() {
		ms, err := s.store.TgMessages(chat)
		if err != nil {
			log.Printf("clear: chat %d: %v", chat, err)
			continue
		}
		if len(ms) == 0 {
			continue
		}
		var ids []int64
		for _, m := range ms {
			if m.At >= cutoff && m.ID != keep[chat] {
				ids = append(ids, m.ID)
			}
		}
		for batch := range slices.Chunk(ids, 100) {
			if err := tg.deleteMessages(ctx, chat, batch); err == nil {
				continue
			}
			// the bulk call refused the batch as a whole — retry one by one
			// so a single undeletable id doesn't keep its neighbours visible
			for _, id := range batch {
				if err := tg.deleteMessage(ctx, chat, id); err != nil {
					log.Printf("clear: delete chat %d msg %d: %v", chat, id, err)
				}
			}
		}
		if err := s.store.DeleteTgMessagesUpTo(chat, ms[len(ms)-1].ID); err != nil {
			log.Printf("clear: forget chat %d: %v", chat, err)
		}
	}
}
