---
tags: [guardian, updates, telegram, rollback]
---

# One-tap update + rollback (v0.19.0)

When [[decisions/guardian-update-watch]] finds a newer **omni** release, the owner now gets a dedicated "🆕 omni vX.Y.Z available" Telegram message with ⬆ Update / 📋 Changelog / 🙈 Ignore buttons instead of the "rerun scripts/install.sh" text line. Other `update_repos` (memoria etc.) keep the text alert; dev builds (`app == omni-dev`) too, since release assets are prod-named.

## Architecture split (forced, not chosen)

The guardian sends via `tgCall` but never long-polls, so it cannot receive taps; the server owns all callbacks but cannot restart itself and survive to report. So: **guardian detects + offers + executes; server receives taps and records intent** as data-dir files — `update.request` (tag to install) and `update.ignore` (tag to skip) — then fires `systemctl --user start --no-block <app>-guardian.service` for an immediate run (falls back to the 2-min timer if that fails).

## Shape

- **Offer dedupe**: new guardian.json key `omni-update` = offered tag, managed only by main() (transitions() untouched), carried across throttled runs like `updates`. Offer sent iff `prev["omni-update"] != tag` → once per tag; a newer release re-offers. The early-return became `msg == "" && !offer && maps.Equal(prev, next)` so key-only changes persist; a failed offer send reverts the key to retry. Value is a tag, not a timestamp — cli renders it as "omni-update — vX available" via `alertLine` (cli/main.go), used by both `omni guardian` and doctor.
- **Server taps** (server/update.go, branches in gatedCallback **before** the bare-uuid fallthrough): `upd:<tag>` writes update.request + starts guardian + "⏳" StripKeyboard; `updlog:<tag>` fetches the release body by tag and posts it in-chat, keyboard kept live; `updign:<tag>` writes update.ignore **and removes updates.stamp** so the guardian re-checks ≤2min and clears its state. All three validate the tag against `^v?[0-9][0-9A-Za-z.\-]{0,30}$`.
- **Executor** (guardian/main.go `runUpdate`, hooked at the top of main() behind `claimUpdateRequest()` — rename-to-.run claim; returns without normal checks): fetch `releases/tags/<tag>` assets → download 3 binaries + checksums.txt to `<data>/update-stage` → sha256 verify → per binary `os.Rename(bin → bin.prev)` then write `.tmp` sibling + rename (atomic, no ETXTBSY) → restart server → `waitServer(tag)` requires /status to report **the new version**, not just health. Fail → restore `.prev`s, restart, `waitServer("")` (any healthy omni counts), report ⚠ rolled-back or 🚨 rollback-also-dead. `.prev` backups stay after success as a manual escape hatch.
- **Probe plumbing**: `probeVersion()` (extends the old probeServer with the version field), `waitServer(want)` and `var probeTries, probeDelay = 15, 3s` (tests shrink the 45s budget); `checkServer` reuses it.
- `sendAll` gained a `kb any` param (nil = old behavior) → `reply_markup.inline_keyboard`.
- `checkUpdates` returns `(checkResult, omniTag, definitive)`; stale prod omni becomes the tag (unless == ignored) and never joins the text detail.

## Non-obvious facts

- The executor's stale-duplicate guard is `version.Version == tag`: after a successful update the guardian binary on disk is new, and a re-delivered tap (server restart re-polls) starts *that* binary, whose compiled-in version now equals the tag. Conversely the *running* pre-update guardian reports the old version in "✅ updated old → new" because its process predates the swap.
- Rollback health check deliberately accepts any version (`waitServer("")`) — demanding the exact old version would fail if `.prev` was missing and the new binary is actually fine.
- systemd serializes same-unit starts, so the tap-fired run and a timer tick can't overlap; claim-by-rename is belt-and-braces.
- GitHub fetches are deliberately duplicated across the two `package main` binaries (server `releaseNotes`, guardian `releaseAssets`) per the repo's twin-function pattern — no shared package for ~20 lines.
- Updates binaries only; a release that changes systemd units still needs scripts/install.sh.

Related: [[decisions/chat-tool-approval-gate]] (the button/idempotency patterns this copies).