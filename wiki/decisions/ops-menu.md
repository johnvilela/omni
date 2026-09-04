---
tags: [telegram, ops, keyboard, systemd]
---

`/ops` (v0.21.0, `server/ops.go`) is one inline keyboard over things omni
already knew how to do — deliberately **not a subsystem**: Status, Doctor,
Logs, Disk, Restart, Update, Terminal, dispatched as `ops:<action>` callbacks
in `gatedCallback` (branch must sit **above** the bare-uuid `resumeSession`
fallthrough, which turns unknown data into a confusing "session not found").
The menu's keyboard stays live so the panel is reusable; only the restart
confirm strips itself.

## What each button reuses
- **Status** — `renderPin(true)` + the version constant. Zero new plumbing;
  even shows the 🖥 TERMINAL line when terminal mode is active.
- **Doctor** — `go s.deliverOneShot(ctx, "", app+" doctor")`: literally the
  `$ omni doctor` one-shot, so streaming progress and `/interrupt` work for
  free. lipgloss/termenv detect the non-TTY pipe and emit plain text — no
  `--plain` flag exists or is needed. The login shell resolves the binary the
  way the user's own shell would (fish config re-adds ~/.local/bin).
- **Logs** — native `journalctl --user -u <app>-server -n 30 -o cat -q` with
  **bot-token redaction** (`botTokenRe`, twin of cli/doctor.go): server log
  lines embed the bot token in transport-error URLs, and this output goes to
  chat. The redaction is load-bearing, not cosmetic.
- **Disk** — 10-line `syscall.Statfs` twin of guardian `checkDisk` plus the
  db file size (two `package main`s can't share).
- **Restart** — see below.
- **Update** — removes `updates.stamp` (un-throttle) **and** `update.ignore`
  (an explicit check means "show me", overriding a previous 🙈), then
  `systemctl --user start --no-block <app>-guardian.service`. The guardian's
  next run re-sends the 🆕 offer from [[decisions/one-tap-update]] if a newer
  release exists; silence = up to date (the guardian has no "up to date"
  reply and growing one wasn't worth it).
- **Terminal** — routes the literal `"/terminal"` through `handleTerminal`,
  landing in the normal sudo-password flow. Guarded: a tap while
  `s.term != nil || s.termPending != nil` replies "already in terminal mode" —
  without the guard the tap's text would be swallowed as a shell command or a
  password attempt.

## The restart replay trap (the one real gotcha)
`Poll`'s `offset` is a **local variable**; Telegram only confirms an update
when the *next* `getUpdates` is issued with a higher offset
(`answerCallbackQuery` does NOT ack). A restart fired synchronously inside a
callback therefore kills the process before the reply sends **and re-delivers
the tap after boot → infinite restart loop**. Also, a plain
`sh -c 'sleep 2; systemctl restart' &` child dies with the server's cgroup
(KillMode=control-group). Fix: `ops:restart!` schedules
`systemd-run --user --on-active=2s --collect systemctl --user restart
<app>-server.service` — a transient unit outside the server's cgroup; the 2s
delay lets the poller issue the next getUpdates (milliseconds after handling)
so the tap is confirmed before the process dies. Restart is also behind a
confirm tap (`ops:restart` → ✅/✖ keyboard) since a mis-tap on a quick panel
is easy.

## Mechanics
- `/ops` case in the `handleMessage` slash switch; registered in
  `setMyCommands` (`telegram_test.go` asserts the exact command list — every
  new command bumps that fixture).
- Callback replies DO carry keyboards: the callback branch of `Poll` falls
  through to `t.send`, which attaches `r.Keyboard` to the last chunk — that's
  what the restart confirm rides on.
- Tests (`server/ops_test.go`) reuse `newUpdateTestServer` (approved pairing
  42, `XDG_DATA_HOME` isolated, PATH replaced by a shim dir). Extra fakes
  (`journalctl`, `systemd-run`) are written **into the same shim dir**,
  recovered as `filepath.Dir(sysLog)`. One test locks the fallthrough trap:
  `ops:bogus` must reply ⚠, not "session not found".

Hierarchy this completes: buttons/chat → `$` one-shot →
[[decisions/terminal-mode]] → physical PC/SSH when omni itself is dead.
