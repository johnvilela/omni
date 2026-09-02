---
tags: [chat, tokens, cost, config, deleted]
---

`defaultTokenBudget` (server/chat.go) went 8k → 100k (Sep 2026). It remains a **cost cap, not a context-window fit**: the chat path re-sends persona + memory + history as one uncached plain prompt every turn, so each message costs up to budget × input price (API) or burns oauth subscription quota (CLI path).

Why not 500k / per-provider budgets: the default chat models are small — gpt-4o-mini has a **128k** window and haiku **200k** — so anything past ~100k would 400-error once history grows. The "1M context" of claude/codex applies to agent sessions and bigger models, not the bare chat path. Per-provider budget config was rejected: the single `token_budget` yaml knob in config.yaml already overrides the default per install.

Side effect: history overflow → memoria compaction (`onCompaction`) now triggers far more rarely, since sessions need ~100k of history before turns get dropped. See [[decisions/context-command-remainder]] — /context shows this budget live.
