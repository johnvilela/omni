---
tags: [tasks, backlog]
---

# Tasks (deferred work)

Deliberate deferrals with their trigger conditions — pick one up when its
trigger fires, not before.

- **Unblock the serial telegram poller.** An agent turn holds `Poll`
  (server/telegram.go) for up to 15 minutes: no `/sessions`, no `/crons`, no
  chat replies meanwhile (messages queue in getUpdates, none are lost).
  Upgrade path already marked by the `ponytail:` comment on `Poll`: answer in
  a goroutine and deliver through `t.send`. Trigger: `/agent` sees real daily
  use and the blocking annoys.
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

Related: [[api]], [[chatbot-memory]]
