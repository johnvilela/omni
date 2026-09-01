---
tags: [api, server, reference]
---

# Server API (localhost)

The omni server listens on `:8787` (override with `OMNI_ADDR`); the CLI reads the same variable to find it. JSON everywhere.

- `GET /status` → `{"app":"omni","version":"..."}` — identity of the running server. There is one omni-wide hand-bumped version, shared by cli and server, in `version/version.go` (shown in `omni --help`); `omni status` compares the server's against the CLI's own and alerts on mismatch (a stale binary).
- `GET /channels` → `[{"name":"telegram","connected":bool,"bot_username":"..."}]`
- `GET /channels/telegram` → same object, single
- `POST /channels/telegram/connect` — body `{"token":"..."}` optional.
  Token resolution order: request body > `TELEGRAM_BOT_TOKEN` env > `~/.config/omni/config.yaml` (`telegram_token:` key).
  - `200` connected (validates via Telegram `getMe`, starts long-poll, persists intent to SQLite)
  - `400 {"error":"token_required"}` — no token anywhere; the CLI prompts and saves it to config.yaml
  - `401` — Telegram rejected the token
- `GET /pairing/telegram` → `[{"user_id":"...","code":"...","approved":bool}]` — every user who ever messaged the bot: approved (paired) and pending
- `POST /pairing/telegram/approve` — body `{"code":"..."}` → `200` the approved pairing | `404 {"error":"unknown_code"}`
- `POST /pairing/telegram/revoke` — body `{"user_id":"..."}` → `200` | `404 {"error":"unknown_user"}` (revoked users start over: next message issues a fresh code)
- `GET /llm` → `[{"name":"openai","connected":bool,"source":"...","default":bool}]` — the three providers (openai, claude, gemini)
- `GET /llm/{provider}` → same object, single; `404 {"error":"unknown_provider"}` for anything else
- `POST /llm/{provider}/connect` — body `{"key":"..."}` optional.
  Credential resolution order: request key > vendor CLI creds file ("oauth") > env (`OPENAI_API_KEY` / `ANTHROPIC_API_KEY` / `GEMINI_API_KEY`) > config.yaml (`openai_key` / `anthropic_key` / `gemini_key`) > (claude only) a `claude` binary on PATH ("claude-code").
  - `200` connected (api keys are live-validated with a models-list call; persists intent to SQLite)
  - `400 {"error":"key_required"}` — nothing resolves; the CLI prompts and saves the key to config.yaml
  - `401` — the provider rejected the api key

Behavior notes:

- **Telegram answers are gated by pairing** (keyed on the sender's user id, `message.from.id`): an unknown sender's first message gets the full pairing instructions (their id + an 8-char code + the `omni pairing approve telegram <code>` command, mirroring Openclaw's flow); while pending they get a short "awaiting approval" reminder; only approved senders reach the llm answer flow. Pairings live in the SQLite `pairings` table; codes use an alphabet without ambiguous chars (no 0/O/1/I/L) and never expire. Unpaired senders are rate-limited: 3 replies per sender per 10-minute window (then silence — the poller sends nothing for an empty answer), and at most 50 pending pairings total (past the cap new senders get a "busy" notice and no row). The limiter is in-memory; a server restart resets it.

- The server long-polls `getUpdates?timeout=50` and answers every text message with the **default llm** — single-turn text in/out, no history, no system prompt. `api_key` sources call the provider API directly with hardcoded cheap models (`gpt-4o-mini` / `claude-3-5-haiku-latest` / `gemini-2.0-flash`); `oauth` and `claude-code` sources shell out to the vendor CLI (`codex exec --skip-git-repo-check` / `claude -p` / `gemini -p`) since their stored tokens aren't usable for direct API calls (gotcha: codex refuses to run outside a trusted/git directory without that flag; its answer goes to stdout, all progress noise to stderr). A faster direct-HTTP route for openai exists but is deliberately deferred — see [[openai-codex-backend]]. While an answer is being produced the bot shows the telegram "typing…" indicator (`sendChatAction` re-sent every 4s — telegram only shows it ~5s per call). No usable llm (no default, or default disconnected) → the bot replies with a `⚠` error notice, never silence. The old rune-reverse echo is gone.
- Connected state lives in SQLite (`channels` table) as *intent*: on restart the server auto-reconnects if the token still resolves. Live status reported by the API is whether a poller is actually running.
- The token is never stored in SQLite — only env/config.yaml, so the secret has one home. Same for llm api keys.
- `OMNI_TELEGRAM_API` env overrides the Telegram API base URL (used by the e2e smoke test with a fake Telegram); `OMNI_OPENAI_API` / `OMNI_CLAUDE_API` / `OMNI_GEMINI_API` do the same for the llm providers (tests only).
- LLM "oauth" = reusing the login a vendor CLI already stored: `~/.codex/auth.json`, `~/.claude/.credentials.json`, `~/.gemini/oauth_creds.json`. Presence+parse check only — no live probe or refresh; that's the vendor CLI's job. omni implements no OAuth flow itself.
- An llm provider is `connected` when the user connected it (intent in SQLite, `llm:<name>` rows in the `channels` table) AND its credentials still resolve; `source` always reports what would be used right now. The telegram answer flow consumes the default provider (see the first bullet).
- LLM providers have no poller, so a server restart needs no resume work for them.
- The default provider lives in config.yaml (`default_llm`), written only by the CLI (`omni llm set-default`); the server just reads it into the `default` field. With no `default_llm` set and exactly one provider connected, that one is reported as the implied default (computed, never written — it adapts when a second connects or creds vanish). It may point at a disconnected provider — deliberate (e.g. config.yaml copied to a new PC); the planned `doctor` command will flag that, not built yet. Backup story: config.yaml alone carries the keys + the default.
