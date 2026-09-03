---
tags: [session, omni, cli, doctor]
lastUsed: 2026-09-03
---

# `omni doctor`: pending-tree commit split, design, and shipping (2026-09-02, 16:43–17:12)

Opened with: "We added the omni-guardian, commit this and lets work on the command called 'doctor'. It will check all the requirements for omni and check if something is missing or print some error message throwed by some service." Explore and Plan agents mapped the whole repo first — the three binaries (`omni`/`omni-server`/`omni-guardian`) sharing one hand-bumped version, the guardian's already-built (but still uncommitted) check set, the CLI's two-stage `route()`/`run()` dispatch, and the fact that `server/config.go` silently zeroes the entire `config.yaml` on any YAML parse error with no error surfaced anywhere — before producing a full doctor design.

## Commit cleanup first

The pending working tree held three already-implemented but uncommitted features (background session queue, `/context`, chat budget clamp) plus the whole guardian watchdog, interleaved across shared file hunks — e.g. `server/store.go`'s DDL hunk added both the queue's `unread` column and `/context`'s `last_ctx` column in one change, and `cli/main.go`'s guardian code touches `renderLLM`, which only compiles once the queue/budget work adds its `BudgetNote` field. Rather than hunk-level surgery to fully separate every feature, the tree was split into 4 commits along boundaries verified to build and test green in isolation, in dependency order:

1. `feat(server)`: background session queue + `/context` + chat budget clamp (one commit — the three interleave in shared hunks), carrying their decision pages ([[decisions/background-session-queue]], [[decisions/chat-budget-model-clamp]], [[decisions/context-command-remainder]]).
2. `feat(guardian)`: the watchdog binary, install scripts, and CLI subcommands — ordered after commit 1 because it depends on the `BudgetNote` field commit 1 adds. Carried the v0.5.0 version bump.
3. `docs(wiki)`: the bump-version rule page and 3 pending session logs.

Commit 1 was verified to build cleanly in an isolated `git worktree` before committing, to confirm the split boundary was actually safe.

## `omni doctor`

Then designed and built `omni doctor` (`cli/doctor.go`), shipped as the 4th commit with the v0.6.0 version bump. Full design detail recorded in [[decisions/doctor-command]]; the command reference lives in [[cli]].

Verified live against the dev install: with the dev server stopped, `doctor` completed in about 0.07s and exited 1 without hanging. Running it against the actually-running dev install surfaced two real findings: a telegram outage earlier that afternoon that guardian had already auto-restarted the server for (its alert was still showing red), and a version mismatch — the installed dev server binary was still v0.5.0, needing a `scripts/dev.sh` re-run to pick up the new v0.6.0 build.

As part of the same commit, `wiki/api.md`, `wiki/tasks.md`, and a new `wiki/cli.md` were edited directly to document doctor's behavior and the newly-closed default_llm-disconnected gap.

Session closed right after the doctor commit landed; the suggested next step (not executed) was running `scripts/dev.sh` to refresh the dev server to v0.6.0.