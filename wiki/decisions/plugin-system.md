---
tags: [plugins, mcp, skills, telegram, decisions]
---

# Plugin system (v0.24.0)

`omni plugins install <owner/repo>` installs one Go binary from the repo's **latest GitHub release** and wires it into omni. Contract doc for plugin authors: `PLUGINS.md` at the repo root (generic — pecunia is only the worked example; it doesn't implement `omni-manifest` yet).

## Shape

- **Install (cli/plugins.go, server-less like the guardian commands)**: pick asset (bare `name_linux_<arch>` beats goreleaser `name_<ver>_linux_<arch>.tar.gz`; linux-only), download to a temp stage, verify against `checksums.txt` when present, run `<staged bin> omni-manifest` (10s) and validate BEFORE installing — so a bad plugin never replaces a good one — then write-rename into `~/.local/bin/<name>`. Manifest snapshot (+ omni-managed `repo`, `skill_dirs`) → `dataDir()/plugins/<name>.json`; repo appended to `update_repos` so the guardian's release watch covers plugin upgrades for free (its stale alert names `omni plugins install <repo>` when a plugins snapshot exists for the binary). Best-effort `POST /plugins/sync` re-publishes the telegram menu on a running server. Upgrade = re-run install (old skill_dirs removed first). `omni plugins` = bubbletea picker → confirm → remove; `omni plugins remove <name>` non-interactive.
- **Manifest contract**: binary prints JSON on `omni-manifest` — name (MUST equal repo basename), version, description, optional `mcp {command,args}` (stdio), `skills: true` (binary supports `omni-skills <dir>`: writes skill dirs into a staging dir omni provides — staged INSIDE the skills root so os.Rename stays on one filesystem), `commands [{name,description,argv}]`.
- **Telegram commands are free-named** (owner decision: "the contract must be generic, the commands can be named anything") within telegram's `^[a-z0-9_]{1,32}$`; collisions with built-ins (list duplicated as `builtinTgCommands` in cli/plugins.go — keep in sync with registerCommands) or other installed plugins are **refused at install**. Dispatch (server/plugin.go `pluginReply`, hooked into handleMessage after the built-in switch, before the @provider/LLM fall-through): normalize `-`→`_`, exec declared argv + whitespace-split user args via `runCLI` (60s cap, `ponytail:` blocks the poll loop — queue it if a plugin gets slow), stdout = reply, ⚠ on error, `✓ (no output)` on silence. `registerCommands(ctx, extra)` appends plugin entries; ConnectTelegram passes `pluginTgCommands()`.
- **MCP + skills scope = agent workspace only** ([[decisions/long-tasks]]'s "zero omni code / vendor user config" note is superseded): `.mcp.json` in `agentDir()` merged via RawMessage maps (foreign entries survive), passed to claude with `--mcp-config` (merges with user config, unlike chat's `--strict-mcp-config`); codex gets `-c mcp_servers.<name>.command/args` TOML overrides per turn (no project mcp file in codex). Cron agent runs + the task runner reuse runClaudeAgent/runCodexAgent so they inherit the wiring. Skills → `agentDir()/.claude/skills/` — **verified live** that claude loads project-level skills from cwd. `skillTokens()` (/context) now globs both skills dirs. Absolute `~/.local/bin` paths everywhere (`resolveBin`) — the systemd unit's PATH lacks it. Chat stays bare; `TestAnswerCLIBare` untouched.

## Prompt commands (v0.25.0)

A manifest command now declares **exactly one** of `argv` and `prompt` (`validateManifest` swapped the argv-must-not-be-empty check for exactly-one-of; structs in both cli/plugins.go and server/plugin.go grew `Prompt`, `omitempty` on both fields). A prompt command dispatches into a fresh agent session — `pluginAgentReply` in server/plugin.go mirrors the `/agent` branch of handleMessage (`agentProvider` → `newSession(true, provider)` → `ensureAgentDir` → `clearChats` → `enqueue`) and answers `⏳ /<name> running (<provider>)`; the real reply arrives async via the queue, so the poll loop never blocks on an LLM turn (the exec path's 60s `ponytail:` cap is unchanged and exec-only). `pluginAgentText` composes the first message: declared prompt, `Owner's message: <raw trailing words>` (NOT word-split — punctuation matters to a prompt), the plan-pages directory (when `memoriaWiki()` is set up), and `cronPrompt(store)` verbatim.

To let such a session manage its own reminders, `applyAgentTools` (server/media.go) generalized its task_start-only loop to an `agentTools` set: task_start + cron_add/cron_edit/cron_delete, executed through the existing `runTool` cases, ungated ([[decisions/chat-tool-approval-gate]]: agent sessions are yolo by design). This grants cron tools to all agent sessions and agent crons too — same trust domain. Known ceiling (`ponytail:` comment): tool confirmations replace TOOL lines in omni's history only, the vendor session never learns the cron ID it created; a later run's fresh `cronPrompt` listing is the recovery path. First consumer: pecunia's `/pecunia-coach`.

## Gotchas hit

- Telegram `setMyCommands` rejects hyphens — that's why command names are underscore-only and dispatch aliases `-` to `_` (`/pecunia-today` works when typed).
- Plugin snapshot structs are duplicated cli/server (project pattern, no shared package); `pluginTestEnv` isolates HOME + XDG_CONFIG_HOME + XDG_DATA_HOME because install touches all three trees.
- Real-pecunia E2E: install fails clean ("not an omni plugin (see PLUGINS.md)") with stderr capped to its first line — pecunia's full usage dump was noise.

Related: [[api]], [[cli]], [[install]], [[decisions/long-tasks]]
