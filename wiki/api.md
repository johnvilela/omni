---
tags: [api, server, reference]
---

# Server API (localhost)

The omni server listens on `:8787` (override with `OMNI_ADDR`); the CLI reads the same variable to find it. JSON everywhere.

- `GET /status` → `{"app":"omni","version":"..."}` — identity of the running server. `version` is the git-describe stamp baked in at build time via ldflags (see [install](install.md)); `omni status` compares it against the CLI's own stamp and alerts on mismatch.
- `GET /channels` → `[{"name":"telegram","connected":bool,"bot_username":"..."}]`
- `GET /channels/telegram` → same object, single
- `POST /channels/telegram/connect` — body `{"token":"..."}` optional.
  Token resolution order: request body > `TELEGRAM_BOT_TOKEN` env > `~/.config/omni/config.yaml` (`telegram_token:` key).
  - `200` connected (validates via Telegram `getMe`, starts long-poll, persists intent to SQLite)
  - `400 {"error":"token_required"}` — no token anywhere; the CLI prompts and saves it to config.yaml
  - `401` — Telegram rejected the token

Behavior notes:

- The server long-polls `getUpdates?timeout=50` and replies to every text message with the text rune-reversed.
- Connected state lives in SQLite (`channels` table) as *intent*: on restart the server auto-reconnects if the token still resolves. Live status reported by the API is whether a poller is actually running.
- The token is never stored in SQLite — only env/config.yaml, so the secret has one home.
- `OMNI_TELEGRAM_API` env overrides the Telegram API base URL (used by the e2e smoke test with a fake Telegram).
