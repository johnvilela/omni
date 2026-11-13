---
tags: [tasks, server, agents, security]
---

# One global cap and daily budget for agent work (SEC-08)

**Status:** DEFERRED — 2026-10-25
**Trigger:** a runaway cron, a stuck task or an unexpected provider bill.
**Branch / PR:** —

## Request

The 2026-10-25 security evaluation (medium): `agentSem` (`server/task.go`) caps only task workers; direct agents, file analysis and crons call `runAgentModel` outside it, crons may overlap, `server/queue.go` has an unbounded pending slice, uploads are buffered whole. No per-day call or token ceiling, no stop switch.

## Notes

- One shared semaphore at `runAgentModel`; skip or serialize overlapping cron runs; bounded queue; per-day ceiling in config plus an `/ops` stop button.
- The HTTP body cap and timeouts already landed with SEC-01 (`server/main.go`).

## Done when

- [ ] Concurrent session, cron, task and analyze_file agents respect one cap.
- [ ] Overlapping cron runs are skipped with a notice.
- [ ] A daily ceiling refuses further model calls predictably; tests use fakes only.
