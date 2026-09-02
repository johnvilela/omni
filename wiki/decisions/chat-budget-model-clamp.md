---
tags: [chat, tokens, cost, config, models]
---

`chatBudget(provider)` (server/chat.go, Sep 2026, supersedes the flat 8k→100k defaults) computes the effective compose budget: `token_budget` from config.yaml (default **500k**) clamped to **80% of the chat model's context window** (`modelWindows` prefix table: claude-fable/opus-5/sonnet-5, gpt-4.1, gemini = 1M; claude = 200k; gpt-5 = 272k; gpt-4o = 128k; unknown = 128k conservative — an optimistic guess would 400-error mid-conversation).

The budget stays a **cost cap**: chat re-sends the whole composed prompt uncached every turn.

Clamp semantics: the 500k *default* clamps **silently** (it just fits whatever model is selected — haiku ends up at 160k, fable at 500k); only an *explicit* `token_budget` that exceeds the window sets `clamped=true`, surfaced as `BudgetNote` on `llmStatus` (rendered by `omni status` / `renderLLM`) and as a ⚠ line in Telegram `/context`.

Gotchas:
- The oauth CLI chat path uses the CLI's own default model, unknowable from omni — `chatModel()` falls back to the cheap `llmModels` default, so the clamp is conservative until a model is picked via `omni llm model`.
- `s.chatProvider(sess)` resolves the sticky pin else the default llm; cron "prompt" jobs use `chatProvider(Session{})`.

See [[decisions/context-command-remainder]] — /context renders this budget live.
