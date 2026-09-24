---
tags: [tasks, llm, codex, models]
---

# Codex model list goes stale

**Status:** DEFERRED — 2026-09-02
**Trigger:** the model pick starts returning 400.
**Branch / PR:** —

## Request

`llmCodexModels` mirrors codex's `/model` picker by hand and rots as codex ships new models. Either fetch the list from codex or add a cheap re-verify step.

## Notes

- Re-verify ritual: ai-memory `gotchas/llm-fallback-model-names-unverified.md`.
- Where the list is served: ai-memory `concepts/server-api.md`.

## Done when

- [ ] The model list offered to users matches what codex currently accepts.
- [ ] A stale entry fails loudly at pick time instead of at the next turn.
