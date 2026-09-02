---
tags: [gotcha, memoria, integration]
---

Found while planning omni's chatbot long-term memory (see [[chatbot-memory]]). Two traps for anything trying to integrate with memoria programmatically (not just as an interactive coding-agent tool):

**Writing a page is MCP-only.** `memoria search` is fully headless-capable (TTY-detected via stdin) and safe to shell out to for recall, but there is no CLI command to write a wiki page — that's `memoria_write_page` over the `memoria mcp` stdio server, or LLM-mediated (`digest`/`process`). The only other option is writing the `.md` file directly: the on-disk format is trivial (`tags:`/`lastUsed:` frontmatter only, memoria-owned; body is plain markdown) and is picked up immediately by search/read, so a process that wants to save durable facts without speaking the MCP protocol can just write the file.

**Global mode isn't automatic, and a registered project always wins.** Global mode was OFF on the dev machine at check time (no `global:` key in `~/.config/memoria/config.yaml`, no `~/.config/memoria/wiki` directory yet). Even with it on, cwd resolution favors a registered project first: since `/home/jv77/Documents/dev/omni` is already registered with memoria, any memoria call made with cwd inside omni resolves to omni's own project wiki, not `_global`. Reaching the global wiki needs either an unregistered cwd with global mode enabled, or `@_global` search selectors.

Also relevant: memoria's hooks are inert unless something actively calls `memoria hook <event>` with a session_id/cwd JSON payload — a process that never calls it produces zero sessions, and `memoria_recall`/`memoria_digest`/`memoria_consolidate` fail gracefully (not crash) when nothing's recorded. Decay only ever sweeps `sessions/`/`trash/sessions/`; any other top-level folder (e.g. a bot's own durable-fact namespace) never decays.