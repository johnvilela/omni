---
tags: [telegram, media, agent, tool-protocol, chat]
---

# Telegram photos & documents (both directions)

Shipped in `v0.8.0` (server/media.go + telegram.go/pairing.go/agent.go/cron.go touches); chat-mode file toolkit added in `v0.9.0`; jailed to the owner's omni folder in `v0.9.1`; binary read_file refusal in `v0.9.2`; `analyze_file` (chat can ask the agent about images/PDFs) in `v0.10.0`. Owner decisions: inbound files flow into the **active session whatever its kind**; outbound sends via explicit **`TOOL:send_file` line**; chat sessions got the full **Create/Send/Read/Edit/Delete** toolkit; all chat file work **confined to `~/.config/omni/files`**; image questions must work from plain chat (no manual /agent).

## Inbound (owner → bot)

- The update struct now decodes `caption`, `photo[]` (last element = largest PhotoSize) and `document` (`file_id`/`file_name`). A third poller hook `tg.file(ctx, from, tgFile)` — sibling of `answer`/`callback`, nil-guarded — carries **metadata only, no bytes**.
- **Pairing gates the download**: `gatedFile` runs `s.gate(fromID)` (extracted from `gatedAnswer`) before any network fetch — an unapproved sender can never trigger a download. This is the whole reason the poller doesn't download.
- `saveInbox` → `getFile` + `GET <apiBase>/file/bot<token>/<file_path>`; the `Telegram` struct keeps `apiBase`/`token` fields for this. Files land in **`filesDir()/inbox/<ts>-<sanitized>`** where `filesDir()` = `~/.config/omni/files` (dev: `omni-dev`) — moved from `agentDir()/inbox` in v0.9.1 so the chat jail reaches received files and the owner has ONE omni folder next to config.yaml. Photos fall back to `photo.jpg`; same-second collisions get a counter. Gotcha: `filepath.Base("")` returns `"."` — `sanitizeName` maps `""`/`"."`/`".."` to `""` so the fallback fires.
- The message becomes `<caption>\n\n[file: /abs/path]` and goes through `handleMessage`.

## Outbound (agent → owner)

- Agent reply line `TOOL:send_file {"path":"/abs/file"}` → `applySendFile` uploads to every approved chat and replaces the line with `📎 sent <name>`. Runs in `agentAnswer` (before the assistant `AddMessage` — history keeps confirmations, never TOOL lines) and in cron **agent** runs. Agent replies get ONLY send_file; **not jailed** (`resolveWorkspace`: absolute anywhere; relative → the workspace cwd).
- Uploads are multipart (`tg.upload`, buffered — telegram caps 50MB). Images → `sendPhoto` with `sendDocument` retry on failure; else `sendDocument`. Files upload immediately even when the session went inactive.
- Contract taught by the workspace seed; `ensureAgentDir` appends the section once to pre-existing CLAUDE.md/AGENTS.md lacking "send_file" — owner edits preserved.

## Chat-mode file toolkit (v0.9.0 → v0.10.0)

Three real incidents drove this: (1) a chat session **hallucinated** creating `/home/jv77/sample.txt` (chat llms are bare — no disk); (2) after tools shipped, a chat model `read_file`'d a JPEG and dumped 8KB of raw bytes into the chat; (3) after the binary refusal, "Explain this image" in chat correctly pointed to /agent — but the owner wants it to just work.

- Six tools run **server-side** off `TOOL:` lines (`fileTool` in media.go, dispatched from `runTool`; `applyTools`/`runTool` carry ctx): `write_file {path,content}`, `read_file {path}` (8KB cap at a rune boundary, single-pass — model uses content next turn; **text only**: NUL byte or invalid UTF-8 → ⚠ binary refusal pointing to analyze_file/send_file), `edit_file {path,find,replace}` (ReplaceAll, count), `delete_file {path}` (regular files only), `send_file`, and **`analyze_file {path,question}`** — a one-shot fresh-context agent run (same machinery as cron agent jobs: `agentProvider()` + `runClaudeAgent`/`runCodexAgent` with empty vendor id, `recordUsage`, no session rows) whose answer replaces the TOOL line; empty question defaults to "Describe it.". Slow (full vendor CLI run inside the chat turn) — the contract tells the model to say it's taking a look.
- **The jail** (`resolveJailed`): every chat tool path is confined to `filesDir()` — relative joins it, absolute must already live inside, `..` escapes refused with `⚠ … path outside <dir>` (prefix check after `filepath.Clean`). `sendFile(ctx, args, resolve)` takes the resolver as a parameter: jailed for chat, workspace for agents.
- `filePrompt()` (func — names the live dir) is injected into every chat prompt beside `cronPrompt`; carries the honesty rule (chat has NO shell/browser/directory access; /agent for those) and steers image/PDF questions to analyze_file.
- Budget-sensitive tests (`TestChatAnswerBudget`, `TestCompaction`) compute overhead as `estTokens("\n\n"+cronPrompt(store)+"\n\n"+filePrompt())` — any new prompt section must join that formula.
- `filesDir()` derives from `os.UserConfigDir()` → tests isolated by `newTestServer`'s `XDG_CONFIG_HOME` tempdir; standalone tests (`TestSaveInboxCollision`) set it themselves. `TestAnalyzeFileTool` fakes the agent CLI via `writeAgentFakes` PATH scripts (config has no default_llm → provider "claude").

## Known limits (ponytail-commented)

- Downloads block the poll loop (≤60s client timeout); hand to the session queue if it hurts.
- read_file answers land a turn late (single-pass tool protocol); analyze_file does NOT — its answer replaces the line directly.
- analyze_file blocks the session queue for the CLI run (same as agent turns).
- Old dev inbox files under `agentDir()/inbox` not migrated (dev-only debris).
- video/voice/audio/stickers still dropped; the same hook can carry them later.

See [[api]] for the reference bullets and [[decisions/pin-dashboard]] for the sibling-hook + s.tg-under-s.mu patterns this reuses.