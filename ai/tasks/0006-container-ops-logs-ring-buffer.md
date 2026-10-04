---
tags: [tasks, server, docker, ops]
---

# `/ops` → Logs has nothing to show in docker

**Status:** DEFERRED — 2026-10-03
**Trigger:** the owner asks for log lines in chat while running omni in docker.
**Branch / PR:** —

## Request

On a host `opsLogs` (`server/ops.go`) reads `journalctl --user -u omni-server`. In the container the server logs to stdout, which only `docker logs` on the host can read, so the button answers with a hint. A small in-process ring buffer (last ~100 `log` lines, token-redacted like today) teed from `log.SetOutput` would make the button work everywhere and drop the journalctl dependency.

## Notes

- Container contract: `ai/rules/container-mode.md`; the hint lives in `opsLogs`'s `inContainer()` branch.
- `omni doctor`'s RECENT ERRORS section has the same gap (`journalChecks` skips when journalctl is missing) — a `/logs` endpoint over the ring buffer would serve both.

## Done when

- [ ] `/ops` → Logs shows the last server lines in docker and on a host alike.
- [ ] `omni doctor` RECENT ERRORS works in docker.
