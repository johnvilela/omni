---
tags: [tasks, server, telegram, security]
---

# Enforce one owner and private chats (SEC-07)

**Status:** DEFERRED — 2026-10-25
**Trigger:** a second identity gets paired, or the bot is added to a group.
**Branch / PR:** —

## Request

The 2026-10-25 security evaluation (medium): `gatedAnswer` (`server/pairing.go`) checks the sender but replies to the originating chat, and the `update` struct (`server/telegram.go`) carries no `Chat.Type`, so an approved user's `/sessions` in a group puts a private session title into that group's inline keyboard. Every approved pairing is equal (`ownerChats`, `server/cron.go`); terminal and proposal state is global.

## Notes

- Fix sketch: add `Chat.Type` to the update struct, refuse group/supergroup/channel contexts (messages, callbacks, uploads), reply only to the private owner chat, and require an explicit ownership change to approve a second identity.
- The audit's probe 6 (`TestSecurityAuditGroupDisclosure`) is the regression test to invert.

## Done when

- [ ] A group message from an approved sender gets no reply and leaks nothing.
- [ ] Callbacks and uploads from non-private chats are refused.
- [ ] Approving a second pairing needs an explicit flag; private flows unchanged.
