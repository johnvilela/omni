---
tags: [gotcha, gemini, llm, cli]
---

# Gemini CLI login is rejected — `IneligibleTierError`

Found live while verifying the bare-CLI flags for the Telegram answer flow (see [[api]], [[sessions/3fc25e35-ce85-4ae2-a550-17d7e425d82f]]): running the `gemini` CLI on this machine, independent of any of omni's `--allowed-mcp-server-names`/`-e none` flags, fails with Google's `IneligibleTierError: This client is no longer supported for Gemini Code Assist for individuals` — Google wants a migration to Antigravity.

Consequence for omni: if `gemini` is ever the default llm, the `oauth`/`claude-code`-style CLI path will fail and the bot replies with its `⚠` error notice (never silence, see [[api]]) until the login is resolved on the Google side. Nothing to fix in omni's code — this is an account/tier problem, not a flag or credential-resolution bug.