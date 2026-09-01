---
tags: [llm, openai, future, reference]
---

# OpenAI via the Codex backend (future upgrade path)

Status: **not built**. Today openai `oauth` answers shell out to
`codex exec --skip-git-repo-check` (see [[api]]) — official, zero auth code,
but pays a few seconds of CLI startup per message. This page records the
faster direct-HTTP route for when that overhead starts to matter.

## The idea

Use the ChatGPT Plus/Pro subscription by speaking to OpenAI's **Codex backend
API** directly with the OAuth tokens the codex CLI already stores — the same
trick opencode's auth plugins use (that's why an OpenAI account "just works"
there: no CLI in the loop, plain HTTP).

## Ingredients

- Tokens live in `~/.codex/auth.json` (verified on this machine, 2026-09):
  `auth_mode`, `OPENAI_API_KEY` (null when subscription-auth), `last_refresh`,
  and `tokens.{id_token, access_token, refresh_token, account_id}`.
- Access tokens expire (~1h). Refresh against OpenAI's OAuth token endpoint
  with the codex CLI's public client id — or lazier: on a 401, run
  `codex exec` once (or any codex command) and re-read auth.json, letting the
  vendor CLI keep owning refresh.
- Requests go to the ChatGPT Codex backend (a Responses-API-shaped endpoint
  under `chatgpt.com/backend-api/codex/`), authenticated with the access token
  plus the `account_id`. Exact endpoint/headers drift — copy them from a
  maintained implementation at build time, don't trust this page's memory:
  - https://github.com/numman-ali/opencode-openai-codex-auth
  - https://github.com/cortexkit/openai-auth

## Why we didn't build it now

- Unofficial, reverse-engineered endpoint: breaks when OpenAI shifts the
  backend; the opencode plugins carry request rewriting, prompt-cache
  stabilizers and quota tracking just to keep up.
- ToS gray zone (Anthropic already enforces against the equivalent trick for
  Claude Max tokens — claude stays on `claude -p`, which Anthropic sanctions).
- The shell-out is one line and bills the subscription correctly today.

## When to build

`codex exec` latency or flakiness becomes a real complaint in telegram chats.
Wire it as a new source inside `askAPI` in `server/answer.go` (openai-only),
keeping `codex exec` as the fallback when the backend call 401s/changes.
