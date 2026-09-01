---
tags: [session, omni]
lastUsed: 2026-09-01
---

# Status command, per-service versioning, dev.sh-as-service (2026-09-01)

Started by diagnosing a report that `scripts/dev.sh` "didn't update the installed CLI or the server" — root cause and rule captured in [[gotchas/dev-vs-prod-binary-names]] (testing was done with `omni` instead of `omni-dev`; the script itself was fine).

Added `omni status`: first explored the existing API/CLI surface (no health/status/version endpoint existed yet; connected-state is purely in-memory `s.cancel != nil` on the server, SQLite only stores connect *intent* and is never cleared on a failed resume). Implemented `GET /status` on the server (`{app, version}`) and a CLI `status` command/screen (server up/down, channel list, alerts). TDD: red tests first, then implementation, then live verification (server up, server down, `--help`, bad arg). This first version included a version-mismatch alert comparing cli vs server version. Committed `dc38204` (server), `055504c` (cli), `4929c1b` (wiki).

The user then required independent per-service versions: "the cli must have its own version, the server must have its own version... We will update it accordingly to changes made on each project." Removed the shared git-describe version stamp from `build.sh` (it now only stamps `app`/`defaultAddr` for dev/prod identity); `cli/main.go` and `server/main.go` each carry their own hand-bumped `version` var, both starting at `v0.1.0`. The CLI help banner now shows the CLI's own version. Consequence: the version-mismatch alert no longer made sense (differing cli/server versions are now expected, not an error) — it was removed, and `omni status`'s ALERTS section reverted to a "no alerts" placeholder. Committed `eab5c32`, `5b062ae`. Resulting contract documented in [[cli]] and [[api]].

Finally, asked why `dev.sh` doesn't start the server too, with the requirement that it mirror `install.sh` and restart a running dev server on re-run. Rewrote `dev.sh` to install binaries and write/enable/restart an `omni-dev-server.service` systemd user unit — same mechanism as prod's `omni-server.service`; `uninstall.sh` updated to stop/remove services per app so `--dev` also removes the dev unit. Verified live: the dev server's PID changed across two consecutive `dev.sh` runs (71652 → 71788) with `:8788/status` answering both times; `uninstall.sh --dev` (declining data deletion) then `dev.sh` brought it back cleanly. `dev.sh` still skips `go test` (fast loop), unlike `install.sh`. Committed `8866a02`, `70a7488`. Details in [[install]].
