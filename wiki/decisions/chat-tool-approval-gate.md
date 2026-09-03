---
tags: [approval, chat-tools, telegram, security]
---

# Chat tool approval gate

Since v0.13.0, privileged `TOOL:` lines in a **normal chat** reply do not execute immediately: the whole reply is parked as a proposal (sqlite `proposals` table) and pushed to the owner with inline buttons — ✅ approve (`appr:<id>`), ✅ always (`alws:<id>`, approve + whitelist), 🚫 deny (`deny:<id>`), ✏ edit (`edit:<id>`). Agent sessions (`/agent`) stay yolo by design — their tools run inside the vendor CLI and are not interceptable.

## Shape

- Gate lives in `chatAnswer` (server/chat.go) before `applyTools`, on **both** llm rounds — the read_file follow-up round is built from file contents (untrusted input) and must not skip it. `applyTools` stays the pure executor.
- **All-or-nothing per reply**: any gated line parks everything (TOOL lines run in order by contract — write then send). On approval `runProposal` executes ALL the reply's TOOL lines, free ones included, in original order.
- Delivery: `proposeTools` pushes via `notifyOwner` with the keyboard; `chatAnswer` returns `""` as the delivered-out-of-band sentinel, `answerNotice` passes it through and `runTask` sends nothing for it. The genuine-empty guard moved from `answerNotice` into `chatAnswer` to free `""` for this.
- Approve **claims by deleting the row first** — a double tap gets "already resolved", never a double execution. Resolved proposals are deleted; pending == row exists; history is the audit trail.
- **Edit is just supersede**: the button only tells the owner to type. Any new message to a session with pending proposals deletes them (top of `chatAnswer`) and leaves a "🚫 pending proposal cancelled" user turn, so the llm revises with the proposal still visible in history.
- History contract: proposal turn = raw reply + "⏳ proposal #N — NOT executed" marker; approve = assistant "✅ owner approved … executed:" + confirmations; deny/supersede = user 🚫 turns. The model always knows what actually ran.

## Config (`~/.config/omni/config.yaml`)

- `approvals: off` — disables the gate. A **string**, not bool: `readConfig()` returns zero Config on any error, and the zero value must mean gate-ON (fail safe).
- `approval_tools: [...]` — which tools are gated; unset = default privileged set `write_file, edit_file, delete_file, cron_add, cron_edit, cron_delete, analyze_file` (read_file/send_file free; analyze_file gated because it spawns a full-permission agent).
- `approval_skip: [...]` — the ✅-always whitelist, **subtracted** from the gated set. Separate key so the button's write is append-only and never clobbers an owner-authored `approval_tools`. Written server-side by `saveConfigValue` (server/config.go), a deliberate twin of cli `saveConfigKey` — any-valued so `token_budget` survives the round-trip.

## In-place message editing (v0.14.0)

`tgReply` grew two button-tap-only flags the poller acts on (plain sends ignore them):

- `StripKeyboard` — after the handler returns, the tapped message's buttons are removed via the new `editMarkup` wrapper (`editMessageReplyMarkup`, empty `inline_keyboard` strips). Set by approve/deny/already-resolved replies, so resolved proposals no longer keep live-looking buttons. NOT set by ✏ edit (still pending).
- `Edit` — the reply **replaces the tapped message** (`editMessage`, which now takes a keyboard; nil kb drops existing markup) instead of sending a new one. Used by `deleteCronCallback`: a 🗑 tap re-renders the /crons listing in place, surviving buttons stay live, and a stale tap heals the outdated list.

The callback update struct now decodes `message.message_id`; per-tap message identity exists only inside the poller, which is why the flags live on `tgReply` rather than handlers calling telegram directly.

## Non-obvious facts found while building it

- Cron "prompt" runs never call `applyTools` (`fireCron` composes without the tool sections) — there is nothing to gate on the cron path.
- `@provider` one-shots and file captions route through `sessionAnswer` → the queue → `chatAnswer`, so they are gated for free.
- Callback data is capped at 64 bytes; `gatedCallback`'s **bare-uuid fallthrough** (resume) swallows any unknown data — new button prefixes must be routed before it.
- Known ceilings (marked `ponytail:`): `runProposal` runs outside the session queue so an approval can interleave history writes; a proposal whose notification was dropped self-heals via supersede.

Related: [[decisions/guardian-update-watch]].