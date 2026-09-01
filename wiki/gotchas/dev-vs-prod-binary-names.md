---
tags: [gotcha, cli, install]
---

# `scripts/dev.sh` installs `omni-dev`, never `omni`

`scripts/dev.sh` only ever builds/installs binaries named `omni-dev` and `omni-dev-server` (and now runs `omni-dev-server` as its own `omni-dev-server.service` systemd user unit, restarting it in place on every re-run) — it never touches `omni` / `omni-server`, which are `install.sh`'s job. This is the dev/prod isolation working exactly as intended (see [[install]] and [[cli]]), but it produced real confusion in this project.

What happened: after running `dev.sh`, the change was checked with `omni --help` (the prod binary) instead of `omni-dev --help`, so stale output kept showing and it was reported as "dev.sh didn't update the installed CLI or the server." Diagnosis (via binary timestamps + shell history) found `omni-dev` *had* updated correctly every time — the check was just run against the wrong binary name, not a script bug. A subsequent full `scripts/uninstall.sh` run (no `--dev` flag) then removed prod entirely, compounding the confusion since `omni` stopped existing at all.

Rule of thumb: after `scripts/dev.sh`, verify with `omni-dev` (e.g. `omni-dev status`), not `omni`. After `scripts/install.sh`, verify with `omni`. Don't assume the two are interchangeable for testing a change.
