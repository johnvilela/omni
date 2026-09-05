---
tags: [install, scripts, reference]
---

# Install layout & scripts

Four scripts in `scripts/` (bash). What lands where:

| | prod (`install.sh`) | dev (`dev.sh`) |
|---|---|---|
| CLI | `~/.local/bin/omni` | `~/.local/bin/omni-dev` |
| Server | `~/.local/bin/omni-server` | `~/.local/bin/omni-dev-server` |
| Guardian | `~/.local/bin/omni-guardian` | `~/.local/bin/omni-dev-guardian` |
| Port | `:8787` | `:8788` |
| Config (token) | `~/.config/omni/config.yaml` | `~/.config/omni-dev/config.yaml` |
| Data (SQLite) | `~/.local/share/omni/omni.db` | `~/.local/share/omni-dev/omni.db` |
| Service | systemd user unit `omni-server.service` (enabled, auto-restart) | systemd user unit `omni-dev-server.service` (enabled, auto-restart) |
| Watchdog | `omni-guardian.timer` + oneshot `.service`, every 2min | `omni-dev-guardian.timer` + oneshot `.service`, every 2min |

- **`build.sh`** — the only place that knows how to build. Env knobs: `APP`, `ADDR`, `OUT`, `PROD=1`. Stamps `main.app` and `main.defaultAddr` via `-ldflags -X` — that is the whole dev/prod isolation mechanism: same code, different compiled identity. The version is NOT stamped: both binaries share the single hand-bumped version in `version/version.go`. Prod adds `-trimpath -s -w`. `build.sh all` builds the release matrix into `dist/` (linux amd64+arm64 only — omni targets linux PCs; both binaries, prod flags, default identity).

## CI / release (GitHub Actions, mirrors memoria's pipeline)

- `.github/workflows/ci.yml` — on PRs to master: `go vet` + `go test -race`, plus a **version-bump gate**: Go/go.mod changes require `const Version` in `version/version.go` to differ from master AND its tag to not exist yet. Note omni's Version carries the `v` prefix (`"v0.4.0"`), so tags use the value verbatim (memoria's is bare and prepends `v`).
- `.github/workflows/release.yml` — on push to master: tests, then if the tag for the current Version doesn't exist: `scripts/build.sh all` → sha256 `checksums.txt` → `gh release create <version> dist/*` with generated notes. Pushing a version bump to master IS the release action; pushing without a bump is a no-op ("tag already exists").
- The release assets are the install contract: `install.sh`, the guardian's one-tap update and `omni plugins install` all read `omni{,-server,-guardian}_linux_{amd64,arm64}` + `checksums.txt` by those exact names — rename them in `build.sh all` and all three break.
- gotestsum deliberately not adopted (memoria uses it for pretty output) — plain `go test -race` keeps zero tool deps.
- No branch-protection ruleset yet — omni commits straight to master today; adopt memoria's `protect-main`-style ruleset (`gh api`, PRs + checks required, no bypass) only if the workflow ever moves to branch+PR.
- **`install.sh`** — a **release downloader, not a source build** (since 2026-09-05; before that it ran `go test` + a gum-driven prod build from a clone). Needs only bash, curl *or* wget, sha256sum and systemd, so it works piped from raw.githubusercontent.com on a bare machine: `curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/install.sh | bash`. Flow: resolve the tag (`OMNI_VERSION` env, else the `releases/latest` redirect via curl — no API call, no rate limit — or the API JSON via wget) → download `omni{,-server,-guardian}_linux_<arch>` + `checksums.txt` from `releases/download/<tag>/` into a `mktemp -d` stage → `sha256sum -c` on just this arch's lines (the file lists both arches) → `install -m755` into `~/.local/bin` (unlink+write, so no ETXTBSY on the running server) → write + enable the systemd user units, restart (so re-running upgrades in place). Two bare-machine guards: if `systemctl --user` can't reach the session bus the units are written and a warning explains how to enable them; if the user doesn't linger, `loginctl enable-linger` is attempted (plain, then `sudo -n`) and otherwise warned about, since user services die at logout on a headless box. `OMNI_SKIP_DEPS=1` skips the trailing block. No gum: output is plain printf with ANSI colors only when stdout is a tty. Then an idempotent **agent dependencies** block (every step `command -v`-guarded, failures warn but never abort): node+npm+chromium via pacman/apt-get, `npm install -g playwright` (its `playwright-cli` bin drives the browser), the agent workspace `~/.local/share/omni/agent/` with its persistent `chrome-profile/` (log into LinkedIn etc. ONCE by hand: `chromium --user-data-dir=<profile>`; the profile is never deleted), the memoria binary from its GitHub latest release (`memoria_linux_{amd64,arm64}`), `memoria setup --client claude-code,codex --global` (hooks into the vendor CLIs + global capture) and a `memoria bootstrap` of the agent workspace. dev.sh installs none of this — the dev machine already has it.
- **`dev.sh`** — dev build of the working tree, installs `-dev` binaries and (re)starts the `omni-dev-server.service` user unit — re-running replaces a running dev server with the new build. No tests: fast loop. Logs: `journalctl --user -u omni-dev-server -f`.
- **`uninstall.sh`** — removes binaries + services (prod and dev units, guardian timer included); asks y/N before deleting config (bot token) and data dirs. Flags: `--dev` (only remove the dev install), `--yes` (skip the prompt). Also gum-free and pipeable: the confirmation reads from `/dev/tty` (stdin is the script when piped), and with no tty and no `--yes` it keeps the dirs.
- **`dev.sh`** is the only script that still needs gum (and Go) — it's for the dev machine, which has both.

## Guardian (watchdog)

`guardian/main.go` — a third binary, out-of-process on purpose: the failure it most needs to catch is the server being dead or hung, so it can't rely on the server's `notifyOwner`. Every 2 minutes (systemd timer) it checks disk, memory, load, network, telegram (`getMe` only — never `getUpdates`, that would steal the server's long-poll), agent process count (pgrep), SQLite `quick_check` + size (read-only handle with its own `busy_timeout`), llm credentials, and `GET /status`. If the server doesn't answer it runs the one heal action it has: `systemctl --user restart <app>-server`, then reports what it did. Alerts go to every approved telegram pairing (token from env/config.yaml, chat ids straight from the pairings table), once per incident plus a recovery notice — state in `~/.local/share/<app>/guardian.json`. A red check is a message, not a unit failure: the guardian always exits 0. Control: `omni guardian` (status), `omni guardian set-interval 5m` (drop-in override, survives reinstall), `omni guardian --enabled=false`. Logs: `journalctl --user -u omni-guardian`.

Gotchas:

- The unit's `ExecStart` uses `%h` (home) — systemd user units don't expand `~`.
- `systemctl --user restart` returns before the port is bound; an immediate CLI call can race for a split second.
- Service management: `systemctl --user status|restart|stop omni-server`, logs via `journalctl --user -u omni-server`.
