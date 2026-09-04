# Omni plugins

An omni plugin is **one Go binary published on a GitHub release** that extends
omni with any of three things:

- an **MCP server** — tools omni's agent sessions (`/agent`, cron agent jobs,
  long tasks) can call
- **Claude skills** — SKILL.md instruction packs the agent loads on demand
- **Telegram commands** — e.g. `/pecunia_today`, answered by executing the
  plugin binary and relaying its output

Pecunia (`github.com/johnvilela/pecunia`) is used as the running example
below; the contract is generic — any repo that follows it is installable.

```
omni plugins install johnvilela/pecunia   # install (or upgrade: same command)
omni plugins                              # list installed, pick one to remove
omni plugins remove pecunia               # non-interactive removal
```

Install downloads the latest-release binary to `~/.local/bin/<name>`, asks it
for its manifest, wires MCP + skills into the **agent workspace**
(`~/.local/share/omni/agent`), enables the declared Telegram commands, and
adds the repo to the guardian's release watch (`update_repos` in config.yaml)
— so a new release triggers an update alert naming the reinstall command.

Chat mode never sees plugins: bare chat sessions have no MCP, no skills, no
tools. Plugins surface in agent sessions and through their Telegram commands.

## Release requirements

- The **latest GitHub release** must carry a Linux binary asset named either
  - `<name>_linux_<arch>` — a bare executable, or
  - `<name>_<version>_linux_<arch>.tar.gz` — a goreleaser-style tarball
    containing the executable named `<name>`

  where `<arch>` is `amd64` or `arm64`. Other platforms are ignored.
- A `checksums.txt` asset (`sha256sum` format) is strongly recommended; when
  present, the downloaded asset is verified against it and a mismatch aborts
  the install.
- `<name>` is the repo basename, and it is also the binary name.
- The binary should answer `--version` printing its version — the guardian
  compares it against the latest release tag to detect stale installs.

## The manifest: `<bin> omni-manifest`

The one required contract. The binary must print a JSON manifest to stdout
(and exit 0) when invoked as `<bin> omni-manifest`:

```json
{
  "name": "pecunia",
  "version": "0.4.0",
  "description": "personal finance tracker",
  "mcp": {"command": "pecunia", "args": ["mcp"]},
  "skills": true,
  "commands": [
    {
      "name": "pecunia_today",
      "description": "today's spending summary",
      "argv": ["pecunia", "today", "--plain"]
    }
  ]
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | MUST equal the repo basename / binary name; install rejects a mismatch |
| `version` | yes | shown in `omni plugins` |
| `description` | yes | shown in `omni plugins` |
| `mcp` | no | how to start the plugin's MCP server (stdio transport). A bare `command` is resolved to `~/.local/bin/`; `args` optional |
| `skills` | no | `true` = the binary supports `omni-skills` (below) |
| `commands` | no | Telegram commands to enable (below) |

A plugin that omits `mcp`, `skills` and `commands` is still installable — it
is just a managed binary with update watching.

## MCP server

Declare `"mcp"` and speak **stdio MCP** on that command (pecunia:
`pecunia mcp`). On install, omni registers it in the agent workspace:

- **claude**: written to `<workspace>/.mcp.json`, passed to every agent turn
  via `--mcp-config` (merges with the user's own servers)
- **codex**: passed as `-c mcp_servers.<name>.*` config overrides

The registered command always uses the absolute `~/.local/bin/<name>` path —
omni's server runs under systemd, whose PATH does not include `~/.local/bin`.
Registration is agent-workspace-scoped on purpose: your other claude/codex
sessions are untouched. If you also want the plugin's MCP everywhere, register
it globally yourself (e.g. `claude mcp add`).

## Skills: `<bin> omni-skills <dir>`

If the manifest says `"skills": true`, install calls the binary as
`<bin> omni-skills <dir>` with an empty directory. Write one directory per
skill into it, each containing a `SKILL.md` (Claude Code skill format —
frontmatter `name:` + `description:`, then the instructions):

```
<dir>/pecunia-overview/SKILL.md
<dir>/pecunia-budget/SKILL.md
```

Omni moves them into `<workspace>/.claude/skills/`, records which directories
you produced, and removes exactly those on uninstall/upgrade — so never write
outside the given directory. Skills load in claude-backed agent sessions
(codex has no skills mechanism).

## Telegram commands

Each `commands` entry enables one command in omni's bot:

- `name`: 1–32 chars of `a-z`, `0-9`, `_` (Telegram's own rules — hyphens are
  impossible in the client menu). Names are **free** — no required prefix —
  but prefixing with your plugin name (`pecunia_today`, not `today`) is
  recommended: install **fails** if a name collides with an omni built-in
  command or with a command of an already-installed plugin. Users may type
  hyphens (`/pecunia-today`); omni treats `-` and `_` as equivalent.
- `description`: shown in Telegram's "/" autocomplete menu.
- `argv`: what omni executes — `argv[0]` is your binary (bare names resolve
  to `~/.local/bin/`), the rest are your fixed arguments. Anything the user
  types after the command is whitespace-split and appended
  (`/pecunia_today last week` → `pecunia today --plain last week`). No shell
  is involved: no quoting, no globbing, no pipes.
- `prompt`: the LLM alternative to `argv` — each command declares **exactly
  one** of the two (omni ≥ v0.25.0; older installs reject a prompt command
  at install). See "Prompt commands" below.

Execution contract:

- stdout is the reply, sent as **plain text** (chunked automatically over
  Telegram's 4096-char limit) — no markdown rendering, so format for a phone
  screen: short lines, no tables.
- non-zero exit → the user sees `⚠` with the error (last stderr line).
- empty stdout on success → the user sees `✓ (no output)`.
- runtime is capped at 60 seconds.
- commands run only for **paired, approved** Telegram users — the same gate
  as everything else in omni.

The menu updates immediately when a running server is reachable at install
time, otherwise on the server's next start.

### Prompt commands

A command that declares `prompt` instead of `argv` does not exec anything:
omni starts a **fresh agent session** (the `/agent` machinery — your plugin's
MCP tools and skills are available) whose first message is the prompt.

- Anything the user types after the command is appended **raw** — one line,
  `Owner's message: <text>`, punctuation intact, no word-splitting.
- Omni then appends its own context: where plan pages live (when memoria is
  set up) and the scheduled-jobs contract with the current job list — so the
  session can write plan pages with its file tools and manage crons by
  emitting `TOOL:cron_add` / `TOOL:cron_edit` / `TOOL:cron_delete` lines.
- The command replies `⏳ /<name> running (<provider>)` immediately; the real
  answer arrives asynchronously through the session queue under the agent cap
  (15 minutes), not the 60-second exec cap. Follow-up messages from the user
  continue the same session — an interview works.
- Write the prompt self-contained: codex-backed sessions load no skills.

## Uninstall

`omni plugins remove <name>` (or the `omni plugins` picker) removes exactly
what install created: the binary in `~/.local/bin`, the `.mcp.json` entry, the
recorded skill directories, the `update_repos` entry and the manifest
snapshot (`~/.local/share/omni/plugins/<name>.json`). Your plugin's own data
(pecunia's SQLite db, config, …) is never touched.
