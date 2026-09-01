---
tags: [install, scripts, reference]
---

# Install layout & scripts

Four scripts in `scripts/` (bash). What lands where:

| | prod (`install.sh`) | dev (`dev.sh`) |
|---|---|---|
| CLI | `~/.local/bin/omni` | `~/.local/bin/omni-dev` |
| Server | `~/.local/bin/omni-server` | `~/.local/bin/omni-dev-server` |
| Port | `:8787` | `:8788` |
| Config (token) | `~/.config/omni/config.yaml` | `~/.config/omni-dev/config.yaml` |
| Data (SQLite) | `~/.local/share/omni/omni.db` | `~/.local/share/omni-dev/omni.db` |
| Service | systemd user unit `omni-server.service` (enabled, auto-restart) | systemd user unit `omni-dev-server.service` (enabled, auto-restart) |

- **`build.sh`** — the only place that knows how to build. Env knobs: `APP`, `ADDR`, `OUT`, `PROD=1`. Stamps `main.app` and `main.defaultAddr` via `-ldflags -X` — that is the whole dev/prod isolation mechanism: same code, different compiled identity. The version is NOT stamped: both binaries share the single hand-bumped version in `version/version.go`. Prod adds `-trimpath -s -w`.
- **`install.sh`** — runs `go test ./...`, prod build, installs binaries, writes + enables the systemd user unit, restarts it (so re-running upgrades in place).
- **`dev.sh`** — dev build of the working tree, installs `-dev` binaries and (re)starts the `omni-dev-server.service` user unit — re-running replaces a running dev server with the new build. No tests: fast loop. Logs: `journalctl --user -u omni-dev-server -f`.
- **`uninstall.sh`** — removes binaries + services (prod and dev units); asks y/N before deleting config (bot token) and data dirs. Flags: `--dev` (only remove the dev install), `--yes` (skip the prompt).

Gotchas:

- The unit's `ExecStart` uses `%h` (home) — systemd user units don't expand `~`.
- `systemctl --user restart` returns before the port is bound; an immediate CLI call can race for a split second.
- Service management: `systemctl --user status|restart|stop omni-server`, logs via `journalctl --user -u omni-server`.
