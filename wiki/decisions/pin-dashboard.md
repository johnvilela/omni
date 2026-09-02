---
tags: [telegram, pin, dashboard, sessions]
---

# `/pin` — pinned live status dashboard (v0.7.0)

`/pin [full|clean]` (server/pin.go) toggles a message pinned in every approved telegram chat, edited in place as session state changes. First line — what telegram's pinned bar shows — is `▶ <active session> · N running · M unread`; `full` mode appends the 5 newest sessions with markers (priority `▶` active > `⏳` running > `✉` unread > `·`), reusing `recentLabel` (extracted from `listSessions`) and `sessionLabel`. `/pin` toggles off (unpin + delete, best-effort — telegram refuses deletes >48h old — rows always cleared); `/pin full|clean` switches mode in place.

## Key decisions

- **Event-driven, no ticker, no timestamps.** `go s.refreshPin()` fires only where display state actually changes: drainer start (`enqueue`), drainer exit (`drain`), unread append (`runTask`), active-pointer moves (`newSession`, `resumeSession`, `ensureSession`), session naming (`nameSession`), and `ConnectTelegram` (covers both server restart and telegram reconnect re-attach). No timestamps in the text ⇒ identical state renders identical text ⇒ dedupe works.
- **Concurrency: one `pinMu` mutex + per-chat `pinLast` text cache, no updater goroutine.** Every trigger renders *current* state, so ordering doesn't matter and bursts coalesce into cache-hit no-ops. Lock order strictly `pinMu → (qmu | s.mu | store)`. `editMessageText` returning "message is not modified" is counted as success (telegram errors on no-op edits).
- **Owner-global, not per-chat.** `handleMessage` carries no chat id; threading one through would ripple ~30 test call sites. The dashboard pins via `s.ownerChats()` like `notifyOwner` — single-owner system.
- **Persistence: SQLite `pins` table** (`chat_id PK, message_id, mode`) in the CREATE list (not the ALTER migration list — that's column-adds only). Restart re-attaches via the ConnectTelegram hook; queues are empty then, so the count self-corrects to 0 (truthful — queued texts already die on restart, see [[decisions/background-session-queue]]).
- **New telegram primitives** in server/telegram.go, each a one-liner over the generic `call`: `sendReturningID` (the chunking `send` discards message_id), `editMessage`, `pinMessage` (disable_notification), `unpinMessage`, `deleteMessage`.

## Known limits (accepted)

- Cron agent runs bypass the queue (`runCron` → agent directly), so they never show as ⏳ running.
- Full-mode list = `RecentSessions(5)`; an older unread session can drop off the list (`ponytail:` comment in renderPin) — the counts line stays globally correct via `Store.UnreadCount()`.

Tests: server/pin_test.go (`fakePinAPI` catch-all recorder; `refreshPin` called synchronously to dodge `go`-hook flakiness under `-race`). Command reference in [[api]].
