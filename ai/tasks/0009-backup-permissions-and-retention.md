---
tags: [tasks, scripts, docker, security]
---

# Owner-only backups and a real delete/retention path (SEC-09)

**Status:** DEFERRED — 2026-10-25
**Trigger:** a backup is copied to shared or cloud storage, or the owner asks to purge history.
**Branch / PR:** —

## Request

The 2026-10-25 security evaluation (medium): `scripts/omni-docker-backup.sh` archives the home and `.env` with the caller's umask (0644 under 022) and no encryption; `/clear` (`server/clear.go`) intentionally keeps sessions, messages and memory, and nothing deletes them.

## Notes

- `umask 077` or `chmod 600` on the archive at creation; optional `age`/`gpg` encryption before leaving the machine.
- A `/forget` or `omni purge` that deletes selected stores, distinct from the visual `/clear`.

## Done when

- [ ] Archives are 0600 regardless of umask; restore to a scratch dir still works.
- [ ] An explicit delete operation covers sessions, messages and inbox; ai-memory stays unless asked.
