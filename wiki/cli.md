---
tags: [cli, reference]
---

# CLI

`cli/` — routing is a hand-rolled two-stage switch (`route()` → `run()` in
cli/main.go), no framework by design (see [[dependencies]]). Help is layered:
`omni help` shows the banner + top-level commands only; every command answers
`--help`/`-h` with its own screen from cli/help.go.

| Command | Does |
|---|---|
| `omni status` | server, channels, llm providers and alerts at a glance; alerts on cli/server version mismatch |
| `omni doctor` | one-shot health report: install, config.yaml validity, systemd units, guardian alerts, server/telegram/llm/pairing, journal errors — each ✗ prints its fix command; exits 1 on any failure; works with the server down (cli/doctor.go) |
| `omni channels` | list/detail/connect message channels |
| `omni llm` | providers, connect, set-default, model |
| `omni pairing` | approve/revoke telegram users |
| `omni guardian` | watchdog status, set-interval, --enabled (see [[install]]) |

Doctor notes: everything derives from the ldflags `app` var (dev vs prod names);
guardian runtime checks are not re-run — their verdict is read from
`guardian.json`; journal errors are a naive keyword grep because the services
log without levels (`ponytail:` marked in cli/doctor.go). Cron schedule
validity is deferred — see [[tasks]].
