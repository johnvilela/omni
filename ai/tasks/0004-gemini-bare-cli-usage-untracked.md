---
tags: [tasks, llm, gemini, usage]
---

# Gemini bare-CLI usage is not tracked

**Status:** DEFERRED — 2026-09-02
**Trigger:** gemini becomes a daily driver.
**Branch / PR:** —

## Request

Gemini's non-interactive output carries no token data, so `/usage` counts nothing for gemini-oauth chat. Probe its `-o json` stats shape and record usage from it.

## Notes

- Usage accounting for the other providers: ai-memory `concepts/server-api.md`.
- Gemini oauth tier gotcha: ai-memory `gotchas/gemini-oauth-ineligible-tier.md`.

## Done when

- [ ] `/usage` reports gemini tokens per chat turn.
