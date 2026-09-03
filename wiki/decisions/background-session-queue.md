---
tags: [queue, telegram, sessions, concurrency]
---

# Background per-session queue for telegram answers

The "unblock the serial telegram poller" deferral landed (was [[tasks]] item #1). All llm answers — chat and agent — now run in background workers; the poller and slash commands stay synchronous and fast.

## Design (server/queue.go)

- The async boundary is in the **Server layer** (`sessionAnswer` → `enqueue`), NOT telegram.go: `t.answer` stays a synchronous hook but returns in ms (enqueue + empty `tgReply` = poller sends nothing). telegram.go and all its tests untouched.
- `Server.queues map[string]*sessionQueue` under `qmu`; **key present = drainer goroutine alive**. One FIFO drainer per session (serializes vendor-CLI resumes — kills the `SetVendorSessionID` fork race); different sessions run concurrently.
- Workers run on `context.Background()` — pollCtx is cancelled on telegram reconnect and must never kill a 15-min agent turn. Per-call timeouts already live in `runCLI`/`askAPI`.
- **The worker re-reads the session row per task** (`store.Session(id)`), never carries the enqueue-time snapshot: claude forks a new `vendor_session_id` every turn, so a stale snapshot forks the vendor conversation. Same reason `chatAnswer(ctx, sess, text)` was split out of `ChatAnswer` — the old body re-resolved the *active* session at run time, which would misroute a queued message after a session switch.
- Delivery on completion: active session unchanged → push the answer via `notifyOwner` (now takes a `tgReply`, so it can carry keyboards). Switched away → append the answer text to the new `sessions.unread` column (text not boolean: `⚠` errors are never persisted as messages; append so two deferred answers both survive) + push "✅ <label> finished" with a resume button whose callback data is the bare session uuid — rides the existing `gatedCallback`→`resumeSession` path for free. `resumeSession` delivers and clears unread.
- `!` prefix = interrupt: cancels the in-flight run of the active session (per-task `context.WithCancel` stored on the queue entry) and pushes the rest of the message to the queue front. A cancelled run delivers **nothing** (no partial output exists — non-streaming CLIs; the killed vendor turn resumes from the last completed turn). Bare `!` just stops.
- Typing indicator moved to `typingOwner` (per approved pairing, whole run duration); `ownerChats()` extracted from `notifyOwner`.

## Gotchas hit

- Tests that gate the fake llm (`promptRecorder.gate`) must **pre-seed the session with one exchange** or the background `nameSession` call eats a gate release and deadlocks the test.
- An abandoned gated handler (interrupted run) keeps `httptest.Server.Close` hanging — release with `close(gate)`, not a send (a send races between the abandoned and the live handler).

## v0.11.0 update: queue persistence, unread-first ordering, same-turn read_file

Shipped together, committed `179fefb`. The queue's original in-memory-only limitation is gone:

- **Queue persistence**: a new sqlite `queue` table (`id, session_id, text`) backs the in-memory queue. `enqueue`/`preempt` insert a row before starting/queuing a task; `drain` deletes the row the instant a task starts — from that point the user turn's own persistence in `messages` is what survives a crash. `replayQueue()` runs once at server boot (before `go srv.runCrons(...)` in main.go) and re-enqueues whatever rows a dead server accepted but never ran. A message that was merely queued (not yet started) when the server died now survives a restart; an in-flight task interrupted mid-run is still lost, same as before.
- **Unread-first session ordering**: `RecentSessions` now orders `(unread != '') DESC, id DESC` instead of pure newest-first. A single query change fixes both the `/pin full` dashboard and the `/sessions` keyboard — an older session holding an undelivered answer can no longer drop off the 5-row cap. See [[decisions/pin-dashboard]].
- **read_file lands same-turn**: `chatAnswer` detects a `TOOL:read_file` line in the llm reply; if it fired, one follow-up llm round runs with the file content already in view, and only that round's answer is returned to the user while history keeps both rounds. Previously the file content only became usable on the model's *next* turn (a ponytail-marked ceiling). Cap is one follow-up round — a `read_file` emitted inside the follow-up still waits a turn.

Related: [[api]], [[tasks]], [[decisions/pin-dashboard]]