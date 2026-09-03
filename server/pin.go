package main

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// handlePin toggles the pinned status dashboard: a message pinned in every
// approved chat, edited in place as sessions start, finish and switch.
// /pin → pin (clean) or unpin; /pin full | /pin clean → set mode, pinning
// if absent. The pinned message id persists in the pins table, so the
// dashboard survives restarts.
func (s *Server) handlePin(ctx context.Context, arg string) tgReply {
	if arg != "" && arg != "full" && arg != "clean" {
		return tgReply{Text: "usage: /pin [full|clean]"}
	}
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	pins, err := s.store.Pins()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return tgReply{Text: "⚠ telegram not connected"}
	}

	if len(pins) > 0 && arg == "" { // toggle off
		for _, p := range pins {
			// best-effort: unpin, then delete (telegram refuses deletes >48h old)
			if err := tg.unpinMessage(ctx, p.ChatID, p.MessageID); err != nil {
				log.Printf("pin: unpin chat %d: %v", p.ChatID, err)
			}
			if err := tg.deleteMessage(ctx, p.ChatID, p.MessageID); err != nil {
				log.Printf("pin: delete chat %d: %v", p.ChatID, err)
			}
		}
		if err := s.store.DeletePins(); err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		s.pinLast = nil
		return tgReply{Text: "dashboard unpinned"}
	}

	if len(pins) > 0 { // mode switch
		if err := s.store.SetPinMode(arg); err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		go s.refreshPin()
		return tgReply{Text: "dashboard: " + arg}
	}

	// create
	mode := arg
	if mode == "" {
		mode = "clean"
	}
	text, err := s.renderPin(mode == "full")
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	if s.pinLast == nil {
		s.pinLast = map[int64]string{}
	}
	for _, chat := range s.ownerChats() {
		id, err := tg.sendReturningID(ctx, chat, text)
		if err != nil {
			log.Printf("pin: send chat %d: %v", chat, err)
			continue
		}
		if err := tg.pinMessage(ctx, chat, id); err != nil {
			log.Printf("pin: pin chat %d: %v", chat, err) // best-effort: still track it
		}
		if err := s.store.SetPin(chat, id, mode); err != nil {
			return tgReply{Text: "⚠ " + err.Error()}
		}
		s.pinLast[chat] = text
	}
	return tgReply{} // the pinned message appearing is the feedback
}

// renderPin builds the dashboard text. No timestamps: identical state must
// render identical text, so unchanged-state refreshes skip the edit call.
func (s *Server) renderPin(full bool) (string, error) {
	active, ok, err := s.store.ActiveSession()
	if err != nil {
		return "", err
	}
	head := "▶ —"
	if ok {
		head = "▶ " + sessionLabel(active)
	}
	unread, err := s.store.UnreadCount()
	if err != nil {
		return "", err
	}
	tasks, err := s.liveTasks()
	if err != nil {
		return "", err
	}
	line := fmt.Sprintf("%s · %d running · %d unread", head, s.runningCount(), unread)
	if len(tasks) > 0 {
		line += fmt.Sprintf(" · %d tasks", len(tasks))
	}
	if !full {
		return line, nil
	}
	// ponytail: capped at 5 rows — unread-first ordering keeps stored answers
	// visible unless more than 5 sessions hold one.
	rs, err := s.store.RecentSessions(5)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(line + "\n")
	for _, r := range rs {
		marker := "·"
		switch {
		case ok && r.ID == active.ID:
			marker = "▶"
		case s.running(r.ID):
			marker = "⏳"
		case r.Unread != "":
			marker = "✉"
		}
		b.WriteString("\n" + marker + " " + recentLabel(r))
	}
	for _, t := range tasks {
		b.WriteString("\n" + taskLine(t, s.workerCount(t.ID)))
	}
	return b.String(), nil
}

// refreshPin re-renders and edits the pinned dashboard in place, best-effort.
// Serialized by pinMu; every trigger renders current state, so bursts
// coalesce into no-op edits caught by the pinLast dedupe. Lock order is
// strictly pinMu → (qmu | s.mu | taskMu | store), never reversed.
func (s *Server) refreshPin() {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	pins, err := s.store.Pins()
	if err != nil || len(pins) == 0 {
		return
	}
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return
	}
	if s.pinLast == nil {
		s.pinLast = map[int64]string{}
	}
	for _, p := range pins {
		text, err := s.renderPin(p.Mode == "full")
		if err != nil {
			log.Printf("pin: render: %v", err)
			return
		}
		if text == s.pinLast[p.ChatID] {
			continue
		}
		err = tg.editMessage(context.Background(), p.ChatID, p.MessageID, text, nil)
		if err != nil && !strings.Contains(err.Error(), "message is not modified") {
			log.Printf("pin: edit chat %d: %v", p.ChatID, err)
			continue // stale until the next event retries — fine
		}
		s.pinLast[p.ChatID] = text
	}
}
