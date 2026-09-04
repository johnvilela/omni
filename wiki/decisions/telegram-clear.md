---
tags: [telegram, clear, sessions, ux]
---

# `/clear` — wipe the telegram chat view (v0.23.0)

`/clear` (server/clear.go) deletes every message omni has seen in every approved chat so the telegram UI looks clean again. **Visual only**: sessions, the `messages` history and memoria are untouched — the model keeps its context. `/new`, `/agent` and a `/sessions` resume tap clear too, before their own reply, so switching sessions always starts from an empty screen.

## Why it works this way

- **Telegram has no "clear chat" for bots.** The only primitive is `deleteMessage`/`deleteMessages` by id, and a bot may delete a message (its own or, in a private chat, the user's) only within **48h** of it being sent. So the server must remember ids: the `seen` hook on `Telegram` records every inbound message id (poller) and every outbound one (`send`, `sendReturningID`, `upload`) into the SQLite `tg_messages` table (`chat_id, message_id, created_at`, PK on the pair). Anything sent before v0.23.0 was never recorded and can't be cleared; anything older than 48h is skipped up front.
- **Owner-global, like the pin dashboard.** `handleMessage` carries no chat id, so the clear iterates `s.ownerChats()`.
- **The pinned dashboard survives.** Ids in the `pins` table are excluded — it's the one message that should outlive a clear, and it shows the session switch that `/new` no longer announces.
- **Silent and best-effort.** Owner's call: delete what telegram allows, log the rest, never report a count. `/clear` and `/new` reply with `DeleteInbound` and no text (the command message disappears too; the empty chat is the feedback). `/agent`'s note and a resume's `resumed: …` + unread answer are sent *after* the clear, as the first message of the clean view.
- **Bulk first, fallback per id.** `deleteMessages` takes 100 ids per call; if telegram refuses a whole batch, each id is retried with `deleteMessage` so one undeletable message doesn't shield its neighbours. Whether the bulk call rejects a batch containing an undeletable id is unverified against the live API — the fallback makes it not matter.
- **Rows are dropped up to the newest id read** (`DeleteTgMessagesUpTo`), not wiped: message ids grow per chat, so anything tracked while the clear was running survives for the next one. A message telegram refused now never becomes deletable, so its row goes too.
- **Order matters around the queue.** `/agent <task>` clears *before* `enqueue`: an answer pushed mid-clear could otherwise be tracked, read and deleted.
- **Pruning is at connect** (`ConnectTelegram` → `PruneTgMessages(now-48h)`) rather than per message — restarts are routine (every update), so the table stays small without a sweep. A periodic sweep is the fix if it ever grows.

## Known limits (accepted)

- Messages from before tracking existed, and anything older than 48h, stay visible — telegram's rule, not omni's.
- Terminal-mode streaming messages (`sendReturningID`) are tracked and cleared like anything else; a `/clear` mid-stream removes the live progress message, the final result still arrives as a fresh send.

Tests: server/clear_test.go (`fakeClearAPI` recorder with per-method failure), `TestTelegramTracksMessageIDs`, `TestStoreTgMessages`. Command reference in [[api]]; related: [[decisions/pin-dashboard]].
