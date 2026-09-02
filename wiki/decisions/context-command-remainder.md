---
tags: [telegram, context, tokens, agent]
---

`/context` (server/context.go) adapts to the active session type:

- **Chat sessions**: mirrors `composePrompt`'s budget walk (persona + memory whole, history newest→oldest) against `token_budget`. No skills/MCP lines — chat CLIs run bare by design.
- **Agent sessions**: bar measured against the model window (claude 200k, codex 272k). The **total is real**: each agent turn stores the CLI-reported context size in `sessions.last_ctx` (claude: input+cache_read+cache_write; codex: last `turn.completed` input). Per-source lines are bytes/4 estimates from disk (workspace CLAUDE.md/AGENTS.md + user-global; skill SKILL.md **frontmatter only** — the body never enters context).

Key decision: **MCP + system tools are never estimated from disk** — tool schemas come from running servers and can't be known statically. Instead they are the *remainder* of the real total minus the estimates. Before the first turn (`last_ctx = 0`) the remainder is unknown and rendered as `?`; the bar falls back to the sum of estimates.

Gotcha: adding a Telegram command means three touch points — `handleMessage` in command.go, `registerCommands` in telegram.go, and the `want` list in `TestTelegramRegisterCommands`.
