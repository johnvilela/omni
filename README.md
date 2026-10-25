<div align="center">

<pre>
 ██████╗ ███╗   ███╗███╗   ██╗██╗
██╔═══██╗████╗ ████║████╗  ██║██║
██║   ██║██╔████╔██║██╔██╗ ██║██║
██║   ██║██║╚██╔╝██║██║╚██╗██║██║
╚██████╔╝██║ ╚═╝ ██║██║ ╚████║██║
 ╚═════╝ ╚═╝     ╚═╝╚═╝  ╚═══╝╚═╝
</pre>

**a simplified, self-hosted messaging hub**

[![ci](https://github.com/johnvilela/omni/actions/workflows/ci.yml/badge.svg)](https://github.com/johnvilela/omni/actions/workflows/ci.yml)
[![release](https://github.com/johnvilela/omni/actions/workflows/release.yml/badge.svg)](https://github.com/johnvilela/omni/actions/workflows/release.yml)
[![latest](https://img.shields.io/github/v/release/johnvilela/omni)](https://github.com/johnvilela/omni/releases/latest)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

</div>

Omni is a personal AI hub you run on your own machine. It puts Claude, Codex and Gemini behind a Telegram bot: quick questions get a bare, tool-less chat answer, while `/agent` opens a full agent session with tools, MCP servers, skills and scheduled tasks. A guardian watchdog keeps the server healthy and offers one-tap updates when a new release lands.

## Features

- **Chat mode** — fast, sandboxed answers with no tool access, straight from Telegram
- **Agent mode** — full Claude/Codex sessions with tools, MCP, skills, browser and terminal
- **Pairing security** — nobody talks to your bot until you approve their one-time code
- **Plugins** — one command installs a Go binary that adds MCP servers, skills and Telegram commands
- **Tasks & crons** — fire-and-forget background jobs and scheduled agent runs
- **Guardian** — watchdog that monitors the server and self-updates from releases (native install; in docker it announces and you pull)
- **Zero-cgo** — pure-Go SQLite, three static binaries, no external database

## Requirements

- Linux (amd64/arm64) with systemd user services — or Docker, see [Docker](#docker) below
- `curl` (or `wget`) and `sha256sum` — that's all the installer needs; no Go, git or gum
- Optional: the `claude`, `codex` or `gemini` CLIs — reused for login and required for agent mode (plain API keys cover chat mode)

## Installation

One command on a bare machine — it downloads the latest release for your CPU and sets everything up:

```sh
curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/install.sh | bash
```

The installer fetches three static Omni binaries from the [latest GitHub release](https://github.com/johnvilela/omni/releases/latest), verifies checksums, and enables the server and guardian user units. It also sets up one local ai-memory service for chat and agent capture. Embeddings and background improvement are disabled for a 4 GB/HDD machine. Login lingering keeps services running after logout. Re-running the installer upgrades Omni in place.

```sh
systemctl --user status omni-server   # should be active
```

Knobs, as environment variables in front of `bash`:

| Variable | Effect |
|---|---|
| `OMNI_VERSION=v0.25.0` | install that release instead of the latest |
| `OMNI_SKIP_DEPS=1` | skip browser-agent dependencies (node, Chromium, Playwright); ai-memory remains enabled |

The server listens on `127.0.0.1:8787` and every API route but `/status` needs a bearer token it generates on first start at `~/.local/share/omni/api-token` (mode 0600). The `omni` cli reads that file when it runs as the same user; `OMNI_API_TOKEN` overrides it.

To remove omni again (it asks before deleting your config and database):

```sh
curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/uninstall.sh | bash
```

## Docker

The other way to run omni: one container holding the server, the guardian, ai-memory and the agent browser stack (node, Chromium, Playwright), with everything you own in one volume. Needs Docker Engine with the compose plugin on linux amd64/arm64 — no systemd, no Go.

```sh
curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/install-docker.sh | bash
```

The installer writes `~/omni-docker/{docker-compose.yml,.env,docker-compose.override.yml}`, asks for the Telegram token, pulls `ghcr.io/johnvilela/omni:latest` and starts it. It **reuses the clis and logins already on your PC**: a standalone `claude` or `codex` binary is bind-mounted read-only into the container, your `~/.claude`, `~/.codex` and `~/.gemini` are shared so there is no second login, and only a cli your PC lacks (or has as a node script, like `gemini`) is npm-installed inside the container's volume. When no `omni` binary exists on the PC it also installs a tiny `~/.local/bin/omni` wrapper around `docker exec`. Re-running the installer is the upgrade — also after you upgrade a cli on the PC, so the mount follows the new version.

By hand, with everything installed inside the container instead:

```sh
mkdir omni && cd omni
curl -fsSLO https://raw.githubusercontent.com/johnvilela/omni/master/docker-compose.yml
curl -fsSL  https://raw.githubusercontent.com/johnvilela/omni/master/.env.example -o .env
docker compose up -d
```

Binaries live in the image, state lives in the `omni-home` volume at `/home/omni` (config, database, plugins, logins, ai-memory, Chromium profile), so `docker compose pull && docker compose up -d` upgrades without touching data.

| | |
|---|---|
| cli | `docker exec -it omni omni status` (or the `omni` wrapper); `plugins`, `doctor` and `guardian` must run this way — they act on the machine they run on |
| login a cli (once, kept in the volume) | `docker exec -it omni claude login` · `docker exec -it omni codex login --device-auth` · `docker exec -it omni gemini` |
| plugins | `docker exec -it omni omni plugins install owner/repo`, or `OMNI_PLUGINS=owner/repo,…` in `.env` (installed/upgraded at every start) |
| logs | `docker logs -f omni` |
| one-off command | `docker compose run --rm omni doctor` |
| shell inside | `docker exec -it omni bash` |
| upgrade | `docker compose pull && docker compose up -d` — the guardian announces new releases on Telegram; [Watchtower](https://containrrr.dev/watchtower/) can pull automatically (it may recreate the container mid-agent-turn) |
| backup / move | `~/omni-docker/omni-docker-backup.sh backup` → one `omni-backup-<date>.tgz` (the whole home volume + `.env`, container stopped meanwhile). On another PC: run the installer there, then `omni-docker-backup.sh restore FILE` — token, keys, plugins and TZ travel; uid, port and the vendor-cli list stay as that PC's installer set them |

Notes: the API port is published on `127.0.0.1` only and every route but `/status` needs the bearer token the server writes to `/home/omni/.local/share/omni/api-token` (the `omni` cli inside the container reads it; `OMNI_API_TOKEN` overrides) — `OMNI_PORT` moves it when a native `omni-server` already owns 8787; `TZ` in `.env` is what crons run in; the container cannot update itself, so the ⬆ Update button becomes a text hint; Chromium runs headless with `--no-sandbox` inside (add `shm_size: 1g` if heavy pages crash); on a 4 GiB box uncomment `mem_limit`; a uid other than 1000 gets a bind-mounted home under `~/omni-docker/home` instead of the named volume; the installer reuses this PC's cli binaries (read-only) but not its `~/.claude`/`~/.codex`/`~/.gemini` — an agent in the container must not be able to read or rewrite your own logins and settings — and `OMNI_SHARE_HOST_CLIS=1 bash install-docker.sh` opts back into sharing them (sessions then also show up in `claude --resume` on the PC). Build locally with `docker build -t omni .` (`--build-arg AI_MEMORY_VERSION=vX.Y.Z` pins ai-memory).

## Setup

1. **Connect Telegram** — create a bot with [@BotFather](https://t.me/BotFather), then:

   ```sh
   omni channels connect -c telegram
   ```

   The token is read from `TELEGRAM_BOT_TOKEN` or prompted for and saved to `~/.config/omni/config.yaml`.

2. **Connect an LLM** — existing `claude`/`codex`/`gemini` CLI logins are reused automatically; otherwise an API key is read from the environment or prompted:

   ```sh
   omni llm connect -p claude        # or: openai, gemini
   omni llm set-default -p claude
   ```

3. **Pair yourself** — message your bot on Telegram. It replies with a one-time pairing code and nothing else. Approve it:

   ```sh
   omni pairing approve telegram <CODE>
   ```

4. **Verify** —

   ```sh
   omni doctor
   ```

Agent sessions run inside the vendor cli's own sandbox by default: Claude gets `--permission-mode acceptEdits` with no one to answer prompts and an allowlist of tools (files, search, web, the browser through `playwright-cli`/`chromium`, omni's MCP servers); Codex gets `--sandbox workspace-write` with network on. Arbitrary shell is denied. If you accept that a page or document an agent reads could steer it with the full reach of your account, set `agent_permissions: full` in `~/.config/omni/config.yaml` to restore `--dangerously-skip-permissions` / `--dangerously-bypass-approvals-and-sandbox`. Either way, children never inherit the bot token, the api token or another provider's keys.

## Usage

| Command | What it does |
|---|---|
| `omni status` | server, channels, llm providers and alerts at a glance |
| `omni doctor` | check install, config, services and llm health — with fixes |
| `omni channels` | manage message channels |
| `omni llm` | manage llm providers (openai, claude, gemini) |
| `omni pairing` | control who may talk to the bot |
| `omni config` | tune omni's behavior (reply persona) |
| `omni plugins` | install and manage plugins (mcp, skills, telegram commands) |
| `omni guardian` | watchdog status, check interval and on/off |
| `omni help` | show the help screen |

In Telegram, plain messages get chat answers; slash commands drive the hub: `/new`, `/clear`, `/agent`, `/task`, `/tasks`, `/sessions`, `/usage`, `/context`, `/crons`, `/pin`, `/terminal` (unprivileged shell as the service user; no sudo), `/interrupt`, `/ops`, `/plan`, `/memory`, `/memory_retention`.

Every AI call Omni makes is captured locally by ai-memory with sanitization and size limits. Raw-observation pruning is set to 30 days by default; ai-memory prunes only sessions it has consolidated, so this is not a guaranteed deletion deadline. Durable `/memory` facts and plans remain pinned. View or change the setting from Telegram with `/memory_retention` or `/memory_retention 45d`.

## Plugins

A plugin is a single Go binary published on a GitHub release. Installing one can add an MCP server and skills to your agent sessions and register its own Telegram commands:

```sh
omni plugins install owner/repo
```

`omni plugins` lists what's installed and removes cleanly. To build your own, see **[PLUGINS.md](PLUGINS.md)** — the whole contract is one JSON manifest your binary prints.

## Development

Building from source needs [Go](https://go.dev) 1.27+. `scripts/dev.sh` needs [Docker](https://docs.docker.com/engine/install/) with the compose plugin and [gum](https://github.com/charmbracelet/gum): it builds the working tree into the `omni:dev` image and runs it as the `omni-dev` container (`docker-compose.dev.yml`: port `127.0.0.1:8788`, volume `omni-dev-home`, settings in a gitignored `.env.dev`), reusing your `claude`/`codex` binaries like the docker installer (logins stay in the volume), and drops an `omni-dev` wrapper in `~/.local/bin`. It also fixes one `OMNI_API_TOKEN` in `.env.dev` and mirrors it to `~/.local/share/omni-dev/api-token`, so a host-built cli (`APP=omni-dev ADDR=:8788 scripts/build.sh`, then `bin/omni-dev status`) talks to the container's api directly; `doctor`, `plugins` and `guardian` still act on the machine they run on and need `docker exec`. It coexists with a production install. `scripts/build.sh` is the single build entrypoint (`PROD=1`, `APP`, `ADDR` knobs; `build.sh all` produces the release matrix). `scripts/uninstall.sh` removes everything (`--dev`: only the dev container), prompting before touching data.

## License

[MIT](LICENSE)
