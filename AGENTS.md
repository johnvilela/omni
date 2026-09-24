Guidance for any AI coding agent (Claude Code, Codex, Cursor, etc.) in this repo. `CLAUDE.md` is `@AGENTS.md`; this file is the single source.

Go module `omni`: `cli/` (the `omni` command), `server/` (localhost API, Telegram bot, sessions, plugins), `guardian/` (watchdog), `version/` (the one hand-bumped version), `scripts/` (install and release).

## The `ai/` folder

Two things live there, nothing else:

- [ai/rules/](ai/rules/) — **binding**. Read before any code change: `bump-version-on-ship`.
- [ai/tasks/](ai/tasks/) — backlog of deferred work, one file per task with a status header and the trigger that un-defers it. See [ai/tasks/README.md](ai/tasks/README.md) for the format.

`ai/skills/` is reserved for scaffolding guides; none exist yet.

## Everything else is ai-memory

Concepts (server API, CLI, install layout, dependencies, chatbot memory), decisions (ADRs), gotchas (traps already hit), audits and session history live in ai-memory, not in the repo. Query it before non-trivial changes (`memory_query`, `memory_read_page`; pages such as `concepts/server-api.md`, `procedures/install-and-release.md`, `gotchas/dev-vs-prod-binary-names.md`). Write there only when the user asks to remember something. Do not recreate a `wiki/`, `concepts/` or `gotchas/` folder in the repo.

## Working agreements

- In plan mode, ask clarifying questions before producing the final plan whenever the requirement is ambiguous.
- Every feature or bugfix bumps `version/version.go` in the same change; CI blocks the PR otherwise.
