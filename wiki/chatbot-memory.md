---
tags: [memory, sessions, memoria, telegram, design]
---

# Chatbot memory

Two layers, the same split Openclaw uses: **short-term** = the conversation itself, re-sent to the llm each turn; **long-term** = durable facts that survive sessions. Built in v0.3.0 (`server/chat.go`, `server/memory.go`).

## Sessions (short-term)

- **Single owner, channel-agnostic**: sessions are global, not keyed by user or chat — omni is a one-person bot, and a session started on telegram can continue on a future channel (discord) for free. The **active session is simply the one with the max id**: ids are uuid7 (`github.com/google/uuid`), so lexicographic order == chronological. A future `session resume` command adds an explicit active pointer; not before.
- Tables: `sessions(id, name, consolidated_until)` + `messages(id, session_id, role, content, created_at)` in the existing SQLite store. The store runs `SetMaxOpenConns(1)` — background goroutines (naming, digest) would otherwise hit SQLITE_BUSY.
- **Names**: after the first exchange, a background llm call titles the session in 3–5 words (`nameSession`); any failure leaves `''`. Display fallback semantic (for the future `session list` picker, not coded yet): first ~5 words of the first user message.
- **Token budget, not message count**: history is estimated at bytes/4 tokens (`estTokens`; overcounts non-ASCII — the safe direction) and walked newest→oldest until `token_budget` (config.yaml, default 8000) is spent. The long-term memory section and the new message always ride whole; an oversized message ships with zero history and lets the provider complain. Exact tokenizers were deliberately rejected: new dep or extra round-trip for a cost cap that doesn't need precision.
- **One composer for every provider** (`composePrompt`): memory + transcript + new message become a single text prompt, sent as-is to the vendor CLIs and as the one user message on the api_key path. Vendor session resume (`codex exec resume`, `claude -p --resume`) rejected — per-provider plumbing for the same result. Degenerate case is load-bearing: no memory + no history → the raw text, byte-for-byte the old single-turn behavior.

## Long-term memory (memoria global wiki)

- Lives in [memoria](https://github.com/johnvilela/memoria)'s **global wiki** as one page: `<wiki>/omni-bot/memory.md`. omni uses **direct file IO** — no subprocess, no MCP client: memoria has no CLI write command, its on-disk format is just `---\ntags: [omni-bot]\n---\n\n<body>`, and pages outside `sessions/` never decay.
- Wiki root: `global_path` from `~/.config/memoria/config.yaml`, else `~/.config/memoria`; wiki = `<root>/wiki`. Dir missing → every memory feature silently no-ops.
- **One-time prerequisite**: `memoria bootstrap --global`. Gotcha: a memoria cron with `auto_apply` sweeps/commits the global wiki on its own schedule — harmless for `omni-bot/` (never decays), just expect commits it makes.
- **READ**: the page body is injected into every prompt (counts against the budget).
- **WRITE — compaction-triggered** (`onCompaction`): turns that fall out of the token budget AND sit above `sessions.consolidated_until` are the moment-of-forgetting overflow; a single-flight background llm call (guard: `Server.digesting`) merges them into a rewritten page ("durable facts, drop chit-chat, ≤300 words, return unchanged if nothing new") and bumps the watermark only after a successful write. Empty/failed digest never clobbers the page and retries on the next overflow. Crash between write and bump re-merges the same turns — the "return unchanged" contract makes that harmless.
- Ceiling (deliberate): turns that never age out of the budget before a session goes quiet never reach long-term memory; end-of-session consolidation arrives with the future session commands.

## Future hook seams

omni will grow its own hook system (like Claude Code / Codex) and memoria will attach to it, capturing chat sessions through its own pipeline. The attachment points are already single-point seams — replace the body, not the answer flow:

- `ensureSession` (server/chat.go) — session created
- `nameSession` (server/chat.go) — session named
- `onCompaction` (server/memory.go) — turns leaving the context window

Related: [[api]], [[dependencies]], [[openai-codex-backend]]
