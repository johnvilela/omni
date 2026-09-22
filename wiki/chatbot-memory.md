---
tags: [memory, sessions, ai-memory, telegram, design]
---

# Chatbot memory

Two layers, the same split Openclaw uses: **short-term** = the conversation itself, re-sent to the llm each turn; **long-term** = durable facts that survive sessions. Built in v0.3.0 (`server/chat.go`, `server/memory.go`).

## Sessions (short-term)

- **Single owner, channel-agnostic**: sessions are global, not keyed by user or chat — omni is a one-person bot, and a session started on telegram can continue on a future channel (discord) for free. The **active session is the one the one-row `active` pointer table names** (set on every create and on `/sessions` resume); the old max-uuid7-id rule survives only as the fallback for pointerless pre-existing DBs. Ids are uuid7 (`github.com/google/uuid`), lexicographic order == chronological.
- Tables: `sessions(id, name, consolidated_until, agent, provider, vendor_session_id)` + `messages(id, session_id, role, content, created_at)` + `active(k=1, session_id)` in the existing SQLite store; the three agent columns arrive via guarded `ALTER TABLE`s on open (errors containing "duplicate column name" ignored). The store runs `SetMaxOpenConns(1)` — background goroutines (naming, digest) would otherwise hit SQLITE_BUSY.
- **Provider pin**: `sessions.provider` on a chat session is the sticky `@provider` pick (empty = default llm); `ChatAnswer` passes it to `answerWith`. Naming and the memory digest stay on the default llm.
- **Agent sessions** (`agent=1`, `/agent`): bypass `composePrompt` and the memory injection entirely — the vendor CLI's own session (persisted in `vendor_session_id`) carries the context — but still log both turns to `messages` and still get named, so `/sessions` and its listing work uniformly. Omni captures their prompt and response directly; ai-memory's vendor hooks additionally capture lifecycle/tool events. See [[api]].
- **Names**: after the first exchange, a background llm call titles the session in 3–5 words (`nameSession`); any failure leaves `''`. Display fallback semantic (for the future `session list` picker, not coded yet): first ~5 words of the first user message.
- **Token budget, not message count**: history is estimated at bytes/4 tokens (`estTokens`; overcounts non-ASCII — the safe direction) and walked newest→oldest until `token_budget` (config.yaml, default 8000) is spent. The long-term memory section and the new message always ride whole; an oversized message ships with zero history and lets the provider complain. Exact tokenizers were deliberately rejected: new dep or extra round-trip for a cost cap that doesn't need precision.
- **Tool section**: `cronPrompt` (server/tools.go) rides the persona slot in every chat prompt — live scheduled-jobs list + the TOOL mutation contract, budget-counted like the persona. This killed the "fresh install sends bare text" degenerate case above `composePrompt` (still intact below it, for utility calls).
- **Persona** (`server/persona.go`): `~/.config/omni/AGENTS.md` (dev: `omni-dev`) leads every composed prompt — chat is stateless, so per-turn injection is what makes the model "never forget" it. Seeded write-once at server boot (`seedPersona` in main.go; owner edits are never clobbered and apply on the next message, no restart). Always included whole, budget-counted like memory. Missing/unreadable file → no persona, no error. Naming and the memory digest stay persona-free.
- **One composer for every provider** (`composePrompt`): persona + memory + transcript + new message become a single text prompt, sent as-is to the vendor CLIs and as the one user message on the api_key path. Vendor session resume (`codex exec resume`, `claude -p --resume`) rejected **for chat mode** — per-provider plumbing for the same result; agent mode adopted it, where the vendor session carrying tool state is the whole point. Degenerate case is load-bearing: no memory + no history → the raw text, byte-for-byte the old single-turn behavior.

## Long-term memory (ai-memory)

- One local [ai-memory](https://github.com/akitaonrails/ai-memory) HTTP service owns the SQLite store and MCP tools. Omni never starts a memory process per request; it uses project scope `omni/assistant`.
- **Capture**: every LLM call Omni starts records the sanitized request and response/error through `/hook`, including chat, follow-up, naming, memory condensation, compaction, cron prompts, agent sessions, task workers and file analysis. Claude/Codex lifecycle hooks add their tool events.
- **Retrieval**: each chat turn queries `memory_query` for a small relevant set instead of injecting the entire memory store. Durable summary lives at `omni/long-term.md`.
- **WRITE — compaction-triggered** (`onCompaction`): turns leaving the token budget are merged into the durable summary page; the watermark moves only after a successful write. Empty/failed digest never clobbers it and retries later.
- **Core memory and plans**: `/memory` keeps the existing themed approval UX, storing pinned pages under `omni/core/`; `/plan` stores pinned pages under `omni/plans/`. Old Memoria pages are deliberately not migrated.
- **Retention**: raw-observation pruning is configured to 30 days. `/memory_retention` shows the value and `/memory_retention 45d` updates Omni config plus the ai-memory service environment. ai-memory only prunes sessions already consolidated into a live summary page, so this is not a guaranteed deletion deadline. Durable pinned pages do not use this raw-observation lifetime.
- **Low-resource profile**: one service, local embedding model fully disabled, no embedding backfill, no automatic improvement scheduler, no scheduled lint, idle I/O priority, and a 512 MiB hard memory limit. This is the install default for the 4 GiB/HDD target.

Related: [[api]], [[dependencies]], [[openai-codex-backend]]
