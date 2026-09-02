---
tags: [cli, doctor, health-check, guardian, config]
---

`omni doctor` (`cli/doctor.go`, shipped v0.6.0) is a one-shot, **report-only** health check: no heal actions (that's guardian's job), every failed check prints its exact fix command, exits 1 if anything failed else 0. Five sections: INSTALL, CONFIG, SERVICES, SERVER, RECENT ERRORS.

## Design principles

- **Everything derives from the `app` ldflags var** (`omni` vs `omni-dev`) — binary names, unit names (`<app>-server.service`, `<app>-guardian.timer`), config/data dirs, and every fix string are built from `app`, never hardcoded, so the same code checks a dev or prod install correctly.
- **Reuse over duplication**: existing CLI/server plumbing is reused as-is (`serverURL()`, `Client.Status()/Channels()/LLMs()/Pairings()`, `systemctlUser()`, the lipgloss styles in `cli/ui.go`). Runtime health (disk/mem/load/net/agent-process-count/sqlite quick_check/db-size/llm-cred-file-presence) is **not** re-implemented in doctor — guardian already runs all of that every 2 minutes (see [[install]]), so doctor just reads guardian's last verdict out of `guardian.json` (via a `guardianAlerts()` helper extracted out of `runGuardianStatus`) instead of duplicating ~300 lines and pulling a sqlite dependency into the CLI.
- **Separate, short HTTP timeout**: doctor's `Client` uses a 5s timeout, not the CLI's normal 30s default — a firewalled-but-not-refused server would otherwise make doctor hang up to 30s per call across several endpoints.
- **Degrades cleanly with the server down**: INSTALL/CONFIG/SERVICES/RECENT ERRORS are pure stat/exec/file-read and always run; only SERVER needs the server. Verified live: with the dev server stopped, `doctor` finished in ~0.07s, printed one ✗ (server not responding, with the restart fix) plus a dim \"skipped\" line for the checks that need it, and exited 1 — nothing hangs.
- **CONFIG is the highest-value section**: `server/config.go` silently zeroes out the *entire* `config.yaml` (including the telegram token) on any YAML parse error, with no error surfaced anywhere else in the system — doctor is the only thing in the project that will ever catch and report that. It checks that the file parses as YAML, that values are typed correctly (the known int/string corruption class from [[gotchas/save-config-key-corrupts-int-keys]]), and that `default_llm` names a known provider.
- **SERVER section closes a documented gap**: a `default_llm` pointing at a disconnected provider was explicitly called out in [[api]] as something doctor would flag once built — now it actually does, alongside app-identity/version-match, telegram-connected, and at-least-one-approved-pairing checks.
- **Token redaction**: the journal excerpts the RECENT ERRORS section prints are redacted to strip bot tokens before printing — telegram API URLs embed the token, and doctor's output is exactly the kind of thing that gets pasted into a bug report.
- **Deferred on purpose**: cron schedule validation (`fireCrons` silently skips an unparseable schedule forever) stays out of doctor — cron rows are only visible server-side and there's no `/crons` HTTP endpoint yet, so it remains a [[tasks]] item with that trigger.

## Shipping

Landed as the last of 4 commits used to clean up a large pending tree in the same session (server queue/context/budget-clamp → guardian → wiki docs → doctor), version-bumped to v0.6.0 per [[rules/bump-version-on-ship]]. Full command reference lives in [[cli]].