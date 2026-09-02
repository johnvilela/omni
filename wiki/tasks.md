---
tags: [tasks, backlog]
---

# Tasks (deferred work)

Deliberate deferrals with their trigger conditions — pick one up when its
trigger fires, not before.

- **Persist the background queue.** Queued-but-unstarted session messages
  live in memory (`Server.queues`, server/queue.go) and die with the server;
  user turns persist only at run time. Marked by the `ponytail:` comment on
  `sessionQueue`. Trigger: a restart eats a queued message and it hurts.
- **OpenAI via the Codex backend** — direct HTTP instead of `codex exec`
  startup cost; see [[openai-codex-backend]]. Trigger: CLI latency becomes a
  real complaint in chats.
- **Codex model list staleness.** `llmCodexModels` mirrors codex's `/model`
  picker and rots; re-verify ritual documented in
  [[gotchas/llm-fallback-model-names-unverified]] and [[api]]. Trigger: model
  pick starts 400ing.
- **Gemini bare-CLI usage untracked** — its non-interactive output carries no
  token data, so `/usage` counts nothing for gemini oauth chat. Trigger:
  gemini becomes a daily driver (then probe its `-o json` stats shape).
- **Doctor can't validate cron schedules.** `fireCrons` silently skips a
  `crons` row with an unparseable schedule forever (server/cron.go), and cron
  rows are only visible server-side — `omni doctor` has no endpoint to read
  them. Trigger: a cron silently never fires (then add a `/crons` route or a
  parse check at insert time).

Related: [[api]], [[chatbot-memory]]
