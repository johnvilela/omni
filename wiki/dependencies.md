---
tags: [dependencies, decisions]
---

# Dependencies

Every non-stdlib package in `go.mod` and why it was added (2026-09-01):

- `modernc.org/sqlite` — SQLite driver for channel state persistence (`~/.local/share/omni/omni.db`). Chosen over `mattn/go-sqlite3` because it is pure Go: no cgo, no C toolchain needed to build.
- `gopkg.in/yaml.v3` — reads/writes `~/.config/omni/config.yaml` (holds the Telegram bot token). YAML was the requested config format; stdlib has no yaml support.
- `github.com/google/uuid` — uuid7 session ids (time-ordered, so the max id is the active session — no active flag needed). Was already in the module graph as an indirect dep of `modernc.org/sqlite`; promoting it to direct cost no new download.
- `github.com/charmbracelet/bubbletea` + `bubbles` + `lipgloss` — explicitly requested for the CLI. `bubbletea` is the TUI runtime, `bubbles/textinput` powers the token prompt, `lipgloss` does all styling. The channel picker is a ~50-line custom bubbletea model instead of `bubbles/list` — with one channel, the full list component (filtering, pagination) wasn't earning its weight.

Deliberately NOT added:

- No Telegram bot library — the server needs exactly three Bot API methods (`getMe`, `getUpdates`, `sendMessage`), all plain JSON over HTTPS via stdlib `net/http` in `server/telegram.go`. If the integration grows (keyboards, media, webhooks), adopt `github.com/go-telegram/bot` (zero-dep, actively maintained, on Telegram's official list). Do NOT use `go-telegram-bot-api` — unmaintained since ~2021.
- No CLI framework (cobra etc.) — command routing is a small switch in `cli/main.go`.
