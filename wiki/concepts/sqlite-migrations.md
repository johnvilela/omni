---
tags: [sqlite, store, migrations, schema]
---

Confirmed live (2026-09-03) when asked whether omni's SQLite store has a migration system and whether shipping an upgrade keeps an existing user DB working. Answer: yes — minimal but deliberate, two idempotent mechanisms in `OpenStore` (server/store.go:28), both run on every boot, **no schema-version counter**.

1. **New tables** ship as `CREATE TABLE IF NOT EXISTS` — on an existing DB the missing table is created and everything already there is untouched; on later boots it's a no-op. The `tasks` table added in this same session (see [[decisions/long-tasks]]) is exactly this case — the safest kind of change.
2. **New columns on already-shipped tables** go through a guarded `ALTER TABLE ADD COLUMN` list (store.go:127-131) — it succeeds with a `DEFAULT` on old DBs, and on fresh installs (where the `CREATE` already includes the column) it fails with "duplicate column name", which is deliberately ignored. This is how `sessions` gained `agent`, `provider`, `vendor_session_id`, `last_ctx`, and `unread` across past releases.

Tested by `TestStoreMigratesOldSchema` (server/store_test.go:211): builds a DB with the ancient pre-agent schema plus real rows, reopens it through `OpenStore`, and asserts the old data survives and the new columns appear.

## The contract for any future schema change

- New table → `CREATE TABLE IF NOT EXISTS` only, nothing else needed.
- New column → must appear in **both** places: the `CREATE` (fresh installs) and a guarded `ALTER` (existing DBs), always with a `DEFAULT` so old rows stay valid.
- Never rename, drop, or retype a column or table — the system is additive-only. That's also what keeps downgrades safe: an older binary simply ignores unknown tables/columns it never reads.

## Ceiling

A genuinely destructive change (rename/retype) is the trigger to add a real versioned migration (a `schema_version` table + ordered steps) — not built yet. Every upgrade so far, including the `tasks` table, has been purely additive, so this has been the correct lazy answer every time.

Related: [[decisions/long-tasks]], [[dependencies]]