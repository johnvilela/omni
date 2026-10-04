---
tags: [tasks, server, queue]
---

# Persist the background session queue

**Status:** DEFERRED — 2026-09-02
**Trigger:** a server restart eats a queued message and it hurts.
**Branch / PR:** —

## Request

Queued-but-unstarted session messages live in memory (`Server.queues`, `server/queue.go`) and die with the server; user turns persist only once the run starts. Persist the queue so a restart replays pending messages.

## Notes

- The gap is marked by the `ponytail:` comment on `sessionQueue`.
- Design of the queue: ai-memory `decisions/background-session-queue.md`.

## Done when

- [ ] Queued messages survive a server restart and run in their original order.
- [ ] No duplicate run for a message that had already started before the restart.
