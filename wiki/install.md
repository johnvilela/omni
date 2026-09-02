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

- **`build.sh`** — the only place that knows how to build. Env knobs: `APP`, `ADDR`, `OUT`, `PROD=1`. Stamps `main.app` and `main.defaultAddr` via `-ldflags -X` — that is the whole dev/prod isolation mechanism: same code, different compiled identity. The version is NOT stamped: both binaries share the single hand-bumped version in `version/version.go`. Prod adds `-trimpath -s -w`. `build.sh all` builds the release matrix into `dist/` (linux amd64+arm64 only — omni targets linux PCs; both binaries, prod flags, default identity).

## CI / release (GitHub Actions, mirrors memoria's pipeline)

- `.github/workflows/ci.yml` — on PRs to master: `go vet` + `go test -race`, plus a **version-bump gate**: Go/go.mod changes require `const Version` in `version/version.go` to differ from master AND its tag to not exist yet. Note omni's Version carries the `v` prefix (`"v0.4.0"`), so tags use the value verbatim (memoria's is bare and prepends `v`).
- `.github/workflows/release.yml` — on push to master: tests, then if the tag for the current Version doesn't exist: `scripts/build.sh all` → sha256 `checksums.txt` → `gh release create <version> dist/*` with generated notes. Pushing a version bump to master IS the release action; pushing without a bump is a no-op ("tag already exists").
- gotestsum deliberately not adopted (memoria uses it for pretty output) — plain `go test -race` keeps zero tool deps.
- No branch-protection ruleset yet — omni commits straight to master today; adopt memoria's `protect-main`-style ruleset (`gh api`, PRs + checks required, no bypass) only if the workflow ever moves to branch+PR.
- **`install.sh`** — runs `go test ./...`, prod build, installs binaries, writes + enables the systemd user unit, restarts it (so re-running upgrades in place). Then an idempotent **agent dependencies** block (every step `command -v`-guarded, failures warn but never abort): node+npm+chromium via pacman/apt-get, `npm install -g playwright` (its `playwright-cli` bin drives the browser), the agent workspace `~/.local/share/omni/agent/` with its persistent `chrome-profile/` (log into LinkedIn etc. ONCE by hand: `chromium --user-data-dir=<profile>`; the profile is never deleted), the memoria binary from its GitHub latest release (`memoria_linux_{amd64,arm64}`), `memoria setup --client claude-code,codex --global` (hooks into the vendor CLIs + global capture) and a `memoria bootstrap` of the agent workspace. dev.sh installs none of this — the dev machine already has it.
- **`dev.sh`** — dev build of the working tree, installs `-dev` binaries and (re)starts the `omni-dev-server.service` user unit — re-running replaces a running dev server with the new build. No tests: fast loop. Logs: `journalctl --user -u omni-dev-server -f`.
- **`uninstall.sh`** — removes binaries + services (prod and dev units); asks y/N before deleting config (bot token) and data dirs. Flags: `--dev` (only remove the dev install), `--yes` (skip the prompt).

Gotchas:

- The unit's `ExecStart` uses `%h` (home) — systemd user units don't expand `~`.
- `systemctl --user restart` returns before the port is bound; an immediate CLI call can race for a split second.
- Service management: `systemctl --user status|restart|stop omni-server`, logs via `journalctl --user -u omni-server`.
