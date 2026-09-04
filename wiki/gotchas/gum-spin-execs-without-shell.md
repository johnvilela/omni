---
tags: [gotcha, gum, shell, scripts]
---

Found while converting `scripts/install.sh`, `scripts/dev.sh`, and `scripts/uninstall.sh` to use gum for UX (see [[install]]). `gum spin -- <cmd>` execs `<cmd>` directly rather than through a shell, so the familiar inline env-var prefix form `PROD=1 scripts/build.sh` does **not** work when passed to `gum spin` — the shell never gets a chance to parse the assignment, so it's treated as part of the command name/args instead.

Fix: wrap with `env`, e.g. `gum spin --show-error --title "building (prod)" -- env PROD=1 scripts/build.sh`. `env`'s job is exactly "set these vars then exec this program", so it works identically whether or not a shell is in the loop.

Confirmed by smoke test before relying on it in the scripts: `gum spin --show-error --title smoke -- env FOO=1 true` succeeds, and `gum spin --show-error --title smoke -- env FOO=1 false` returns the wrapped command's exit code (non-zero) — proving `set -e` still aborts an install/build on failure through the spinner, not just that the env var was set.

Rule of thumb: any `gum spin`/`gum exec`-style wrapper that needs an env var set for one invocation must go through `env VAR=value cmd`, never the bash-only `VAR=value cmd` prefix form.