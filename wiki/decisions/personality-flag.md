---
tags: [config, persona, style, cli]
---

# `personality` config flag (v0.16.0, CLI in v0.17.0)

`personality:` in config.yaml switches omni's reply style: `quiet` (terse but human — short complete sentences, lead with the answer, no filler) or `ultraquiet` (telegraph-minimal, fragments fine). Unset/unknown/unreadable config = normal — the string zero value is the fail-safe, same stance as `approvals`. Config is re-read every turn, so changes apply without restart.

Set it with **`omni config persona`** (cli/main.go): interactive picker showing the current value, or `-p quiet|ultraquiet|normal` to skip it — the `omni llm set-default` pattern (select + flag + `saveConfigKey`). `omni config` is a new command namespace for behavior tuning; `readConfigKey` (cli) is the read-side twin of `saveConfigKey`. No telegram toggle yet.

## Where it hooks

- **`readPersona()` (server/persona.go) is the choke point**: it appends `personalityPrompt()` after the owner's AGENTS.md body (later instructions win over the seed's softer style section). That one edit styles chat answers (`chatAnswer`), cron `prompt` jobs (`fireCron`) and keeps `/context` token accounting consistent — no call-site changes.
- **Agent sessions bypass the persona** (raw text to the vendor CLI), so `agentAnswer` appends the one-line `personalityMarker()` to the wire text only — `AddMessage`/`nameSession` keep the owner's original words (locked by `TestAgentAnswerPersonality`).
- One-shot agent runs (analyze_file, cron `agent`, long-task steps/summaries) deliberately untouched: their prompts/workspace seed already demand concise output.

Gotcha: if AGENTS.md is missing, `readPersona()` returns "" early and no style section is injected — fine in practice because `seedPersona()` writes it at every boot.

Related: [[decisions/long-tasks]], [[cli]], [[api]]