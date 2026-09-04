---
tags: [readme, license, github, docs]
---

The repo previously had no `README.md` and no `LICENSE` — `AGENTS.md`/`CLAUDE.md` are memoria memory pointers, not project documentation, and `PLUGINS.md` (see [[decisions/plugin-system]]) only documents the plugin contract.

## What was added

- **`README.md`** at repo root: an ASCII-shadow OMNI banner (reusing the CLI's own banner style) centered on top, a tagline, badges (ci, release, latest version, MIT license), a short description, a features list, requirements, an install guide, a setup guide, a CLI command table, the Telegram slash-command list, a plugins section linking to `PLUGINS.md`, dev/uninstall notes, and the license section.
- **`LICENSE`** — MIT, so GitHub's license detection and badge resolve.
- **GitHub repo metadata**, via `gh repo edit`: description set to "Self-hosted AI messaging hub — talk to Claude, Codex & Gemini from Telegram, with agent sessions, tasks and plugins"; topics added: `go`, `telegram-bot`, `self-hosted`, `llm`, `claude`, `mcp`, `ai-agent`, `personal-assistant`, `sqlite`.

## Accuracy points the README had to get right

- `scripts/install.sh` **builds from source** (git clone + `go test` + a `gum`-driven install) — it is NOT a release downloader. GitHub releases (via `.github/workflows/release.yml`) are consumed only by the guardian's one-tap self-update path and by `omni plugins install`, never by first-time install. The README's install guide states this explicitly rather than implying a `curl | sh` one-liner.
- The setup guide follows the real flow: `omni channels connect -c telegram` → `omni llm connect -p <provider>` → `omni llm set-default -p <provider>` → the owner runs `omni pairing approve telegram <code>` for the first paired sender → `omni doctor` to verify. Access control is per-user pairing codes, not a single configured chat id.

## Shipping

Committed as `63cc10e` — "docs: add readme with install and setup guides + mit license" — on `master`, pushed directly (no branch mishap this time — see [[gotchas/stale-branch-before-commit]] for the earlier incident in the same session). Docs-only, so no version bump: consistent with [[rules/bump-version-on-ship]], which only requires a bump for feature/bugfix code changes. The release workflow ran on the push and correctly skipped cutting a release since the version tag was unchanged.