---
tags: [gotcha, bash, fish, tooling]
---

# The Bash tool always runs bash, even though the user's shell is fish

While smoke-testing `omni llm set-default` against a throwaway server, a scratch script used fish syntax (`set -x SCRATCH /tmp/...`) inside the Bash tool. The Bash tool executes bash, not fish, so `set -x` was parsed as bash's shell-tracing flag instead of a variable assignment — `$SCRATCH` stayed empty, `XDG_CONFIG_HOME` resolved to `/config`, and the write failed with `mkdir /config: permission denied`. Fixed by rewriting the script with plain bash syntax (`SCRATCH=/tmp/...`), after which everything passed.

Rule of thumb: always write Bash-tool commands in bash syntax for this project, regardless of the user's interactive shell (fish) — never `set -x VAR value`, use `VAR=value`.