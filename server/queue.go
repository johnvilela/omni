package main

import (
	"context"
	"log"
)

// sessionQueue is one session's background work: the in-flight task's cancel
// (nil when between tasks) plus the messages waiting behind it. Pending texts
// are mirrored in the queue table so a restart replays them (replayQueue).
type sessionQueue struct {
	cancel  context.CancelFunc
	pending []queuedMsg
}

// queuedMsg is one waiting text plus its queue-table row id (0 when
// persisting failed — the message still runs, it just won't survive a
// restart).
type queuedMsg struct {
	id   int64
	text string
}

// enqueue persists text and queues it for one session.
func (s *Server) enqueue(sessID, text string) {
	id, err := s.store.AddQueued(sessID, text)
	if err != nil {
		log.Printf("queue: persist: %v", err)
	}
	s.enqueueMsg(sessID, queuedMsg{id, text})
}

// enqueueMsg queues one message and starts the session's drainer when none is
// running. One drainer per session (FIFO — serializes vendor CLI resumes);
// different sessions run concurrently.
func (s *Server) enqueueMsg(sessID string, m queuedMsg) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.queues == nil {
		s.queues = map[string]*sessionQueue{}
	}
	q, running := s.queues[sessID]
	if !running {
		q = &sessionQueue{}
		s.queues[sessID] = q
	}
	q.pending = append(q.pending, m)
	if !running {
		go s.drain(sessID)
		go s.refreshPin()
	}
}

// replayQueue re-enqueues texts a previous run accepted but never started.
// ponytail: a preempt-prepended text replays in plain id order — fine.
func (s *Server) replayQueue() {
	rows, err := s.store.QueuedMessages()
	if err != nil {
		log.Printf("queue: replay: %v", err)
		return
	}
	for _, r := range rows {
		s.enqueueMsg(r.SessionID, queuedMsg{r.ID, r.Text})
	}
}

// preempt handles a "!"-prefixed message: cancel the active session's
// in-flight task and run text next. Empty text just stops.
func (s *Server) preempt(text string) tgReply {
	sess, err := s.ensureSession()
	if err != nil {
		return tgReply{Text: "⚠ " + err.Error()}
	}
	s.qmu.Lock()
	q, running := s.queues[sess.ID]
	if running {
		if text != "" {
			id, err := s.store.AddQueued(sess.ID, text)
			if err != nil {
				log.Printf("queue: persist: %v", err)
			}
			q.pending = append([]queuedMsg{{id, text}}, q.pending...)
		}
		if q.cancel != nil {
			q.cancel()
		}
		s.qmu.Unlock()
		if text == "" {
			return tgReply{Text: "⏹ stopped"}
		}
		return tgReply{Text: "⏹ interrupted — running your message"}
	}
	s.qmu.Unlock()
	if text == "" {
		return tgReply{Text: "nothing running"}
	}
	s.enqueue(sess.ID, text)
	return tgReply{}
}

// drain runs one session's queue to empty, then unregisters itself.
func (s *Server) drain(sessID string) {
	for {
		s.qmu.Lock()
		q := s.queues[sessID]
		if len(q.pending) == 0 {
			delete(s.queues, sessID)
			s.qmu.Unlock()
			go s.refreshPin()
			return
		}
		m := q.pending[0]
		q.pending = q.pending[1:]
		ctx, cancel := context.WithCancel(context.Background())
		q.cancel = cancel
		s.qmu.Unlock()

		// the task is starting: the user turn persists inside it, so the
		// queue row has done its job
		if err := s.store.DeleteQueued(m.id); err != nil {
			log.Printf("queue: %v", err)
		}
		s.runTask(ctx, sessID, m.text)
		cancel()

		s.qmu.Lock()
		q.cancel = nil
		s.qmu.Unlock()
	}
}

// runTask answers one queued message and delivers the result: directly when
// its session is still active, else stored as unread plus a tap-to-resume
// notification. Runs on its own context — a telegram reconnect must not kill
// a 15-minute agent turn; per-call timeouts live in runCLI/askAPI.
func (s *Server) runTask(ctx context.Context, sessID, text string) {
	stop := s.typingOwner(ctx)
	defer stop()
	sess, ok, err := s.store.Session(sessID)
	if err != nil || !ok {
		log.Printf("task: session %s: gone (%v)", sessID, err)
		return
	}
	var answer string
	if sess.Agent {
		answer = s.agentAnswer(ctx, sess, text)
	} else {
		answer = s.answerNotice(ctx, sess, text)
	}
	if ctx.Err() != nil {
		log.Printf("task: session %s: interrupted", sessID)
		return // preempt already acked; the killed run has no answer worth sending
	}
	active, ok, err := s.store.ActiveSession()
	if err == nil && ok && active.ID == sessID {
		s.notifyOwner(ctx, tgReply{Text: answer})
		return
	}
	// err falls through here too: storing beats losing the answer
	if err := s.store.AppendSessionUnread(sessID, "\n\n"+answer); err != nil {
		log.Printf("task: %v", err)
	}
	go s.refreshPin()
	s.notifyOwner(context.Background(), tgReply{
		Text:     "✅ " + sessionLabel(sess) + " finished",
		Keyboard: [][]button{{{Text: "▶ resume", CallbackData: sessID}}},
	})
}

// typingOwner keeps the "typing…" indicator alive in every approved chat
// while a background task runs; no-op when telegram isn't connected.
func (s *Server) typingOwner(ctx context.Context) (stop func()) {
	s.mu.Lock()
	tg := s.tg
	s.mu.Unlock()
	if tg == nil {
		return func() {}
	}
	var stops []func()
	for _, id := range s.ownerChats() {
		stops = append(stops, tg.typing(ctx, id))
	}
	return func() {
		for _, f := range stops {
			f()
		}
	}
}

// sessionLabel is a session's display name: llm-given title, else a short id.
func sessionLabel(sess Session) string {
	name := sess.Name
	if name == "" {
		name = sess.ID
		if len(name) > 8 {
			name = name[:8]
		}
	}
	if sess.Agent {
		name = "🤖 " + name
	}
	return name
}

// running reports whether a session has background work in flight or queued.
func (s *Server) running(sessID string) bool {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	_, ok := s.queues[sessID]
	return ok
}

// runningCount is the number of sessions with background work.
func (s *Server) runningCount() int {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return len(s.queues)
}
