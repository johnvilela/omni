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
- **Guardian** — systemd watchdog that monitors the server and self-updates from releases
- **Zero-cgo** — pure-Go SQLite, three static binaries, no external database

## Requirements

- Linux (amd64/arm64) with systemd user services
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

To remove omni again (it asks before deleting your config and database):

```sh
curl -fsSL https://raw.githubusercontent.com/johnvilela/omni/master/scripts/uninstall.sh | bash
```

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

In Telegram, plain messages get chat answers; slash commands drive the hub: `/new`, `/clear`, `/agent`, `/task`, `/tasks`, `/sessions`, `/usage`, `/context`, `/crons`, `/pin`, `/terminal`, `/interrupt`, `/ops`, `/plan`, `/memory`, `/memory_retention`.

Every AI call Omni makes is captured locally by ai-memory with sanitization and size limits. Raw-observation pruning is set to 30 days by default; ai-memory prunes only sessions it has consolidated, so this is not a guaranteed deletion deadline. Durable `/memory` facts and plans remain pinned. View or change the setting from Telegram with `/memory_retention` or `/memory_retention 45d`.

## Plugins

A plugin is a single Go binary published on a GitHub release. Installing one can add an MCP server and skills to your agent sessions and register its own Telegram commands:

```sh
omni plugins install owner/repo
```

`omni plugins` lists what's installed and removes cleanly. To build your own, see **[PLUGINS.md](PLUGINS.md)** — the whole contract is one JSON manifest your binary prints.

## Development

Building from source needs [Go](https://go.dev) 1.27+ and [gum](https://github.com/charmbracelet/gum). `scripts/dev.sh` builds the working tree and installs a parallel `omni-dev` stack (own port `:8788`, own config and database) that coexists with your production install. `scripts/build.sh` is the single build entrypoint (`PROD=1`, `APP`, `ADDR` knobs; `build.sh all` produces the release matrix). `scripts/uninstall.sh` removes everything, prompting before touching data.

## License

[MIT](LICENSE)
