---
tags: [plan, memory, approval, telegram, wiki]
---

# /plan and /memory commands (v0.22.0)

Two commands riding existing machinery end to end: the TOOL protocol, the approval gate (proposals + ✅/🚫/✏ buttons — the required approve/deny/request-changes flow, for free), the cron `agent` kind, `startTask`, and ai-memory MCP pages.

## /plan (server/plan.go)

- **Interview in the chat session**: `/plan <goal>` sets `sessions.plan` (guarded ALTER) and enqueues the goal; `chatAnswer` injects `planContract()` while the flag is set (named apart from task.go's `planPrompt`). Deny/✏ leave the flag — the interview continuing IS the change-request loop; it clears when `plan_save` executes (both the `runProposal` and approvals-off paths).
- **Tap answers**: a `TOOL:ask {"question","options"}` line in the reply becomes an inline keyboard pushed out-of-band (`parseAsk` hook in chatAnswer, after the gate check — gate wins when both appear; raw turn parked in history first). Buttons carry `opt:<option>` (`truncBytes` 60-byte rune-safe cap under telegram's 64-byte callback limit); `gatedCallback` routes `opt:` into `sessionAnswer` — a tap answers exactly as if typed. `askVisible` appends the question when the model's prose left it out.
- **No plans table**: a plan is a pinned ai-memory page `omni/plans/<slug>.md`; body markers (`Status: active|done`, `#long`) are the whole state machine, scraped by `planMeta`. `plansPrompt()` (always injected) lists slugs + the `plan_start` contract.
- **Approval saves ONLY** — `plan_save` writes the page and nothing else. Execution needs the owner to ask: gated `plan_start` → tag `long` ⇒ daily 09:00 `agent` cron running `dailyPlanPrompt(path)` (execute next step, update ## Progress vs ## Target, report); else ⇒ `startTask` (checkpointing/pause/resume for free).
- **PLAN DONE**: the daily agent ends its reply with last line `PLAN DONE`; `fireCron`'s agent branch calls `planDone` and deletes its own cron (`c.ID` in hand — no linkage row needed). `parseControl` deliberately NOT reused: its verb whitelist rejects `PLAN`.

## /memory (server/memory.go)

- **Theme-paged core memory**: pinned ai-memory pages at `omni/core/<theme>.md`, one fact bullet per approval. `general` is always injected whole; other themes are listed by name+count only and **sticky-loaded per session** via free (ungated) `TOOL:memory_load` → `sessions.themes` (comma-joined) — a work chat never sees family facts. `applyTools`/`runTool` carry the session id (empty from session-less call sites means memory_load refuses).
- **/memory [theme:] <text>** bypasses the chat queue: background `s.Answer` condense ("Reply exactly <theme>|<fact>", existing themes offered; single-word `theme:` prefix forces it), then a hand-built `TOOL:memory_save` reply through `proposeTools` on the active session — buttons, ✏ supersede loop and history coherence all inherited. Approvals off ⇒ direct `applyTools`.
- **Never condensed**: `onCompaction` only rewrites `omni/long-term.md`, so pinned `omni/core/` pages are excluded by construction.
- **Persona must stay first** (`TestChatAnswerInjectsPersona` asserts prefix) — core memory is a sibling section; "HIGHEST priority" is prose in the section, not position.

## Gotchas

- New always-on prompt sections must be mirrored in BOTH `overhead` expressions (`TestChatAnswerBudget` chat_test.go, `TestCompaction` memory_test.go), and every new slash command in `registerCommands` must land in the `want` fixture (telegram_test.go).
- Facts saved in a session live in that session's history anyway (the ✅ executed confirmation) — theme scoping shows its value only in OTHER sessions; TestMemoryCommand asserts on a fresh session for this reason.
- Gate additions: `plan_save`, `plan_start`, `memory_save` in `defaultApprovalTools`; `memory_load` and `ask` stay free.

Related: [[decisions/chat-tool-approval-gate]], [[decisions/long-tasks]], [[chatbot-memory]]
