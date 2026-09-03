package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the deadline hits.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached in time")
}

// seedSession creates the active session with one past exchange so the
// background nameSession call never fires (it would eat a gate release).
func seedSession(t *testing.T, srv *Server) Session {
	t.Helper()
	sess, err := srv.ensureSession()
	if err != nil {
		t.Fatal(err)
	}
	srv.store.AddMessage(sess.ID, "user", "seed", 1)
	srv.store.AddMessage(sess.ID, "assistant", "seeded", 2)
	return sess
}

// TestQueueSerializesPerSession: the dispatcher returns instantly while the
// llm call is still blocked, and two messages to one session run in order.
func TestQueueSerializesPerSession(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	rec.gate = make(chan struct{})
	sess := seedSession(t, srv)

	if reply := srv.handleMessage(context.Background(), "msg1"); reply.Text != "" {
		t.Fatalf("msg1 = %q; want queued silence", reply.Text)
	}
	if reply := srv.handleMessage(context.Background(), "msg2"); reply.Text != "" {
		t.Fatalf("msg2 = %q; want queued silence", reply.Text)
	}
	// both accepted while the first llm call is still gated
	rec.gate <- struct{}{}
	rec.gate <- struct{}{}
	waitFor(t, func() bool { return !srv.running(sess.ID) })
	ms, _ := store.Messages(sess.ID)
	if len(ms) != 6 || ms[2].Content != "msg1" || ms[3].Content != "pong" ||
		ms[4].Content != "msg2" || ms[5].Content != "pong" {
		t.Fatalf("messages = %+v; want msg1 then msg2 exchanges in order", ms)
	}
}

// TestQueueDeferredDelivery: a task finishing after the user switched
// sessions stores its answer as unread and pushes a tap-to-resume notice;
// resuming delivers and clears it.
func TestQueueDeferredDelivery(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	rec.gate = make(chan struct{})
	sent := make(chan map[string]any, 16)
	fake := fakeTelegram(t, sent, nil, nil, "")
	t.Cleanup(fake.Close)
	srv.mu.Lock()
	srv.tg = NewTelegram(fake.URL, "TOKEN")
	srv.mu.Unlock()
	store.AddPairing("telegram", "99", "CODE1234")
	store.ApprovePairing("telegram", "CODE1234")

	sessA := seedSession(t, srv)
	srv.handleMessage(context.Background(), "long task")
	waitFor(t, func() bool { return len(rec.all()) == 1 }) // in flight
	srv.handleMessage(context.Background(), "/new")        // switch away
	rec.gate <- struct{}{}

	notice := <-sent
	text, _ := notice["text"].(string)
	if !strings.Contains(text, "finished") {
		t.Fatalf("notice = %q; want a finished notice", text)
	}
	raw, _ := json.Marshal(notice["reply_markup"])
	if !strings.Contains(string(raw), sessA.ID) {
		t.Fatalf("notice keyboard = %s; want resume button for %s", raw, sessA.ID)
	}
	if s2, _, _ := store.Session(sessA.ID); !strings.Contains(s2.Unread, "pong") {
		t.Fatalf("unread = %q; want the stored answer", s2.Unread)
	}

	reply := srv.resumeSession(sessA.ID)
	if !strings.Contains(reply.Text, "resumed") || !strings.Contains(reply.Text, "pong") {
		t.Fatalf("resume = %q; want resumed + the unread answer", reply.Text)
	}
	if s2, _, _ := store.Session(sessA.ID); s2.Unread != "" {
		t.Fatalf("unread after resume = %q; want cleared", s2.Unread)
	}
}

// TestQueuePreempt: "! msg" cancels the in-flight run (its answer is
// discarded, no unread) and runs the new message next.
func TestQueuePreempt(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	rec.gate = make(chan struct{})
	sess := seedSession(t, srv)

	srv.handleMessage(context.Background(), "msg1")
	waitFor(t, func() bool { return len(rec.all()) == 1 }) // in flight
	reply := srv.handleMessage(context.Background(), "! msg2")
	if !strings.Contains(reply.Text, "interrupted") {
		t.Fatalf("! reply = %q; want an interrupted ack", reply.Text)
	}
	// unblock both the abandoned msg1 handler and msg2's call
	close(rec.gate)
	waitFor(t, func() bool { return !srv.running(sess.ID) })
	ms, _ := store.Messages(sess.ID)
	// seed pair + msg1's user turn (persisted before the kill, no assistant
	// answer) + msg2's full exchange
	if len(ms) != 5 || ms[2].Content != "msg1" || ms[3].Content != "msg2" || ms[4].Content != "pong" {
		t.Fatalf("messages = %+v; want msg1 unanswered, msg2 answered", ms)
	}
	if s2, _, _ := store.Session(sess.ID); s2.Unread != "" {
		t.Fatalf("unread = %q; interrupted run must deliver nothing", s2.Unread)
	}
}

// TestQueuePreemptBare: "!" alone just stops the in-flight run.
func TestQueuePreemptBare(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	rec.gate = make(chan struct{})
	sess := seedSession(t, srv)

	if reply := srv.handleMessage(context.Background(), "!"); reply.Text != "nothing running" {
		t.Fatalf("idle ! = %q; want nothing running", reply.Text)
	}
	srv.handleMessage(context.Background(), "msg1")
	waitFor(t, func() bool { return len(rec.all()) == 1 })
	if reply := srv.handleMessage(context.Background(), "!"); reply.Text != "⏹ stopped" {
		t.Fatalf("! = %q; want ⏹ stopped", reply.Text)
	}
	close(rec.gate)
	waitFor(t, func() bool { return !srv.running(sess.ID) })
	if ms, _ := store.Messages(sess.ID); len(ms) != 3 || ms[2].Content != "msg1" {
		t.Fatalf("messages = %+v; want only msg1's user turn added", ms)
	}
}

// TestQueuePersistsRows: an accepted-but-unstarted text has a queue row; the
// row is deleted the moment its task starts.
func TestQueuePersistsRows(t *testing.T) {
	srv, store, rec := newChatTestServer(t)
	rec.gate = make(chan struct{})
	sess := seedSession(t, srv)

	srv.handleMessage(context.Background(), "msg1")
	waitFor(t, func() bool { return len(rec.all()) == 1 }) // msg1 started: its row is gone
	srv.handleMessage(context.Background(), "msg2")
	rows, _ := store.QueuedMessages()
	if len(rows) != 1 || rows[0].Text != "msg2" || rows[0].SessionID != sess.ID {
		t.Fatalf("queue rows = %+v; want just msg2", rows)
	}
	close(rec.gate)
	waitFor(t, func() bool { return !srv.running(sess.ID) })
	if rows, _ := store.QueuedMessages(); len(rows) != 0 {
		t.Fatalf("queue rows after drain = %+v; want none", rows)
	}
}

// TestQueueRestartReplays: rows left by a dead server run on the next start.
func TestQueueRestartReplays(t *testing.T) {
	srv, store, _ := newChatTestServer(t)
	sess := seedSession(t, srv)
	// simulate the previous run's leftovers: rows in the table, no drainer
	store.AddQueued(sess.ID, "msg1")
	store.AddQueued(sess.ID, "msg2")

	srv.replayQueue()
	waitFor(t, func() bool { return !srv.running(sess.ID) })
	ms, _ := store.Messages(sess.ID)
	if len(ms) != 6 || ms[2].Content != "msg1" || ms[3].Content != "pong" ||
		ms[4].Content != "msg2" || ms[5].Content != "pong" {
		t.Fatalf("messages = %+v; want replayed msg1 then msg2 exchanges", ms)
	}
	if rows, _ := store.QueuedMessages(); len(rows) != 0 {
		t.Fatalf("queue rows = %+v; want none after replay", rows)
	}
}
