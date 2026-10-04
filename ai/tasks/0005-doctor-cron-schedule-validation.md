---
tags: [tasks, cli, doctor, cron]
---

# Doctor cannot validate cron schedules

**Status:** DEFERRED — 2026-09-02
**Trigger:** a cron silently never fires.
**Branch / PR:** —

## Request

`fireCrons` silently skips a `crons` row with an unparseable schedule forever (`server/cron.go`), and cron rows are only visible server-side, so `omni doctor` has no endpoint to read them. Add a `/crons` route for doctor, or a parse check at insert time, or both.

## Notes

- Doctor design: ai-memory `decisions/doctor-command.md`.

## Done when

- [ ] An unparseable schedule is rejected at insert time with a clear error.
- [ ] `omni doctor` lists cron rows and flags any that will never fire.
