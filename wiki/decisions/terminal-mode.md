---
tags: [telegram, shell, sudo, fish]
---

`/terminal` runs shell commands on the omni-server host straight from chat,
bypassing the LLM (v0.18.0, `server/terminal.go`). "Other PC" == the server host
itself: Telegram is outbound-only, no SSH, no second machine — commands exec
locally.

## Runs as the user, sudo auto-authenticated
Commands run **as the service user**, not root, so the user's own shell config
loads (fish functions, zoxide `z`, mise, aliases). Running as root loaded root's
config instead → `Unknown command: z` and mise "not trusted" noise (the bug that
drove this decision). `sudo` is made non-interactive: `autoSudo` rewrites a
**leading `sudo ` → `sudo -A `**, and the command runs with `SUDO_ASKPASS` pointed
at a tiny helper (`askpassScript`, written once to tempdir) that echoes
`$OMNI_ASKPASS_PW`; `sudoEnv(pass)` puts that var in the process env. So the
password lives only in the shell process's **environment (memory), never on
disk**. A mid-pipeline sudo isn't rewritten (ceiling — use a leading sudo).

## Shell selection (fish / bash / zsh)
Login shell resolved by `defaultShell()` via `getent passwd <uid>` field 7
($SHELL is a fallback). Sentinel probe adapts per shell (`shellProbe`): fish uses
`set __c $status … (git …)`, bash/zsh/sh the POSIX `__c=$? … "$(git …)"`.

**Two execution models (the "Mixed" decision):**
- **bash/zsh — persistent shell.** One long-lived `<shell>` process (as the user)
  fed line-by-line over a pipe; `cd`/env/bg-jobs persist. `cmd`/`stdin`/`out` set;
  spawned via `newShell(sudoEnv(pass), shell)`.
- **fish — per-command.** Fish **can't** be a persistent piped shell: it reads
  stdin to **EOF before executing**, and only runs incrementally with a real
  **PTY**, whose output is flooded with line-editor redraw codes (unparseable). So
  each fish command runs as a fresh `fish -c` (`runOnce`, `cmd==nil`) with
  `exec.Cmd.Dir` = a **tracked cwd** carried between commands and `Env=sudoEnv`.
  `fish -c` sources `config.fish`, so `z`/mise/abbreviations load. Trade-off: env
  vars and background jobs don't persist.

The validated password is cached on `termSession.pass` for the session (for
`sudo -A`), cleared on teardown.

## `$ <cmd>` one-shot
Runs as the user via `<login-shell> -c` (already never root). No sudo by default;
if the command matches `\bsudo\b` it prompts for the password **every time**
(never cached), rewrites a leading `sudo` to `sudo -S -p ''` and pipes it via
stdin. `runOneShot` takes the shell as a param (test seam — fish rebuilds `$PATH`
from config, so tests pin `/bin/bash` for the fake-sudo path).

## Prompt line (all paths)
Every reply is prefixed with **`📁 <last-two-segments> (<git-branch>)`** (e.g.
`📁 dev/omni (master)`), then a blank line, the output, `[exit N]` when non-zero.
`formatShellReply`/`parseSentinel`; path trimmed by `shortPath`.

## Mechanics / reuse
- `handleTerminal` at the **top of `handleMessage`** (`server/command.go`), before
  the `!` preempt and slash switch. `applyPassword` handles the awaiting-password
  step; **wrong password re-prompts up to `maxSudoTries=3`** (`termPending.tries`)
  then gives up.
- Ephemeral `Server` state (`termMu`, `term`, `termPending`). One `termWorker`
  drains `cmds` (buffered 64; non-blocking send so the poller never blocks) →
  `exec()` dispatches to persistent `run()` or per-command `runOnce()`. Output via
  `notifyOwner` + `typingOwner`.
- `/exit` → `endTerminal` (kills the persistent process if any, closes `cmds`);
  `exit`/EOF ends a persistent shell naturally. Password message deleted via the
  **`DeleteInbound`** flag on `tgReply` (interpreted by `Poll`, reuses
  `deleteMessage`); needed `message_id` on the inbound update struct.

## Ceilings (ponytail)
- **Non-interactive only** (no PTY): vim/top/`apt` prompts don't work.
- **fish loses env/bg-job persistence** (only cwd) — inherent to fish's EOF-slurp;
  a PTY + terminal-emulator parser was rejected as too fragile.
- **sudo password sits in the shell process env** during the session (memory, not
  disk) so `sudo -A` can auth non-interactively.
- Persistent shell has no per-command timeout (`/exit` force-kills); fish `runOnce`
  and `$` use a 5-min timeout. Ungated beyond the pairing gate (owner-only).

Owner-only is automatic: everything reaching `handleMessage` passed
`gate()`/`gatedAnswer` (`server/pairing.go`).
