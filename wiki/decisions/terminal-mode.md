---
tags: [telegram, shell, sudo, fish]
---

`/terminal` runs shell commands on the omni-server host straight from chat,
bypassing the LLM (v0.18.0, `server/terminal.go`). "Other PC" == the server host
itself: Telegram is outbound-only, no SSH, no second machine — commands exec
locally. v0.20.0 added `/interrupt` (^C), a 5-min idle TTL, live output
streaming, and a pinned-dashboard indicator.

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
  fed line-by-line over a pipe; `cd`/env/bg-jobs persist. Spawned via
  `newShell(sudoEnv(pass), shell)` — since v0.20.0 with `Setpgid: true` and
  `trap ':' INT` fed as an init line (see interrupt below).
- **fish — per-command.** Fish **can't** be a persistent piped shell: it reads
  stdin to **EOF before executing**, and only runs incrementally with a real
  **PTY**, whose output is flooded with line-editor redraw codes (unparseable). So
  each fish command runs as a fresh `fish -c` (`runOnce`, `cmd==nil`) with
  `exec.Cmd.Dir` = a **tracked cwd** carried between commands and `Env=sudoEnv`.
  `fish -c` sources `config.fish`, so `z`/mise/abbreviations load. Trade-off: env
  vars and background jobs don't persist.

The validated password is cached on `termSession.pass` for the session (for
`sudo -A`), cleared on teardown.

## /interrupt = ^C without killing the session (v0.20.0)
- **Persistent shell**: the shell runs in its own process group (`Setpgid`);
  `/interrupt` = `syscall.Kill(-shellpid, SIGINT)`. The load-bearing trick is
  **`trap ':' INT`** fed at startup: a *handler* survives in the shell but is
  reset to default in children, so the running command dies (`[exit 130]` via the
  already-queued sentinel line) while the shell lives. `trap '' INT` would NOT
  work — SIG_IGN is inherited across exec, children would ignore ^C too.
  Verified by live experiment: without the trap, non-interactive bash dies with
  its child. Ceiling: a builtin-only loop (`while :; do :; done`) has no child to
  kill — `/exit` is the hammer. zsh assumed equal (untested here, no zsh).
- **fish / `$` one-shots**: per-command `exec.CommandContext` with `Setpgid` +
  custom `c.Cancel` = group SIGINT + **`c.WaitDelay = 10s`** (mandatory once
  custom output writers replace CombinedOutput — a backgrounded grandchild
  holding the merged pipe would hang `Wait` forever; `groupInterrupt(c)` helper).
  The in-flight cancel is stored on `termSession.cancel` / `Server.oneShotCancel`
  under `termMu`; `/interrupt` calls it. Concurrent `$` one-shots: last one wins
  the stored cancel (ponytail).
- Routing: inside terminal mode `handleTerminal` owns `/interrupt`; outside, a
  `case "/interrupt"` in the `handleMessage` slash switch reaches a running
  one-shot. Both run on the poller goroutine, never blocked by the busy worker.

## Idle TTL + teardown race (v0.20.0)
`termIdleTTL = 5min` (`time.AfterFunc` armed by `startTerminal`, `Reset` on every
queued command and every completion). `expireTerminal` checks `!ts.busy` and
tears down **in the same termMu critical section** (no TOCTOU); busy sessions
survive the timer. Key structural change: `endTerminal` is now fully under
`termMu` with an `s.term == ts` idempotence guard, and `handleTerminal`'s
channel send happens **under termMu after re-checking `s.term == term`** —
before v0.20.0 only the poller closed `cmds`, the TTL timer made the unlocked
`close` a send-on-closed-channel panic vector. `/exit` during a fish command now
also cancels it (endTerminalLocked calls `ts.cancel`).

## Output streaming (v0.20.0)
`streamProgress(buf)` per command (terminal worker AND `$` one-shots): after
`streamAfter=5s` one "🖥 running · Ns" message per owner chat
(`sendReturningID`), edited every `streamEvery=3s` with elapsed + a 3500-byte
rune-safe `tail` of a mutex-guarded `streamBuf` (persistent `run` writes each
line into it — the sentinel read-loop was already line-incremental; fish/one-shot
paths use `io.MultiWriter(&raw, buf)` as Stdout+Stderr). The returned
`finish(final)` edits the progress message in place when final ≤4096 bytes, else
deletes it and sends normally (chunked); no progress message yet → plain
`notifyOwner`. finish blocks until the ticker goroutine exits (ids handed over a
1-buffered channel), so the final reply is always the last message. No rate
limiting (ponytail — one edit/3s is fine).

## Pinned dashboard indicator (v0.20.0)
`renderPin` prepends `🖥 TERMINAL · <shortPath(cwd)>` while `s.term != nil`. The
cwd is **not** read off the session (data race): `run`/`runOnce` return the
sentinel's pwd up to `termWorker`, which publishes `Server.termCwd` under
`termMu`. **Lock order extended: pinMu → (qmu | s.mu | taskMu | termMu | store)**
— never call refreshPin while holding termMu; all triggers are
`go s.refreshPin()` after unlock (enter/exit/expiry/each command). `refreshPin`
now no-ops on nil store (zero-value `&Server{}` tests).

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
  then gives up. `/interrupt` while awaiting the password is consumed as a
  password attempt (accepted edge; `/exit` cancels).
- Ephemeral `Server` state (`termMu`, `term`, `termPending`, `termCwd`,
  `oneShotCancel`). One `termWorker` drains `cmds` (buffered 64 — queueing is a
  feature) → `exec()` dispatches to persistent `run()` or per-command `runOnce()`.
- Test knobs are package vars: `termIdleTTL`, `streamAfter`, `streamEvery`
  (tests shrink + restore). Streaming/TTL/pin tests reuse the recording
  `fakePinAPI`/`newPinServer` harness from `pin_test.go`.
- Password message deleted via the **`DeleteInbound`** flag on `tgReply`.

## Ceilings (ponytail)
- **Non-interactive only** (no PTY): vim/top/`apt` prompts don't work.
- **fish loses env/bg-job persistence** (only cwd) — inherent to fish's EOF-slurp.
- **sudo password sits in the shell process env** during the session (memory, not
  disk) so `sudo -A` can auth non-interactively.
- Persistent shell has no per-command timeout (`/interrupt` or `/exit`); fish
  `runOnce` and `$` use a 5-min timeout that now group-SIGINTs. Builtin-only
  loops shrug off SIGINT. Ungated beyond the pairing gate (owner-only).

Owner-only is automatic: everything reaching `handleMessage` passed
`gate()`/`gatedAnswer` (`server/pairing.go`). Update-button flow lives in
[[decisions/one-tap-update]]; command/API conventions in [[api]].
