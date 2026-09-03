---
tags: [gotcha, testing, ci, shell]
---

Found while diagnosing a CI failure in `TestAgentAnswerSendFile` (`server/agent_test.go`) — see [[sessions/a646597e-6f70-4b9c-ad6f-090c2278776c]]. The test fakes the `claude` CLI as a `#!/bin/sh` script built from a Go string: `script := "#!/bin/sh\necho '" + result + "'\n"`, where `result` is a JSON string containing a `\n` escape (`{"result":"done\nTOOL:...","session_id":"v1"}`).

On the dev machine (Arch), `/bin/sh` is a symlink to bash, whose `echo` prints backslashes literally — the `\n` stays a valid two-character JSON escape, so the test passed locally. On the Ubuntu CI runner, `/bin/sh` is dash, whose `echo` interprets `\n` as a real newline byte (POSIX XSI `echo` behavior) — a raw newline inside a JSON string is illegal, so `json.Unmarshal` failed with `invalid character '\n' in string`, surfaced through `parseClaudeJSON`/`agentAnswer` as `claude: bad json output: ...`. Confirmed identically under alpine's POSIX `sh` via `docker run`.

Fix: replace `echo '...'` with `printf '%s\n' '...'` in the script generator — `printf`'s `%s` argument is never escape-expanded by any POSIX shell, so it behaves the same under dash and bash.

Rule of thumb: any test fixture that fakes a CLI as a shell script and embeds a string containing backslash escapes (JSON, regex, etc.) must build the script with `printf '%s\n'`, never `echo` — a dev machine's `/bin/sh` being bash hides a divergence that Ubuntu CI's dash (or any POSIX-strict `/bin/sh`) will hit.