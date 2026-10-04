---
tags: [tasks, llm, openai, codex]
---

# OpenAI answers through the Codex backend over HTTP

**Status:** DEFERRED — 2026-09-02
**Trigger:** CLI latency becomes a real complaint in chats.
**Branch / PR:** —

## Request

Call the Codex backend directly over HTTP for openai-oauth chat answers instead of paying the `codex exec` process start-up cost on every turn.

## Notes

- Why not now and how later: ai-memory `notes/future-openai-codex-backend.md`.
- Provider wiring: ai-memory `concepts/server-api.md`.

## Done when

- [ ] openai-oauth chat turns answer without spawning `codex exec`.
- [ ] Fallback to the CLI path stays when the HTTP path fails.
