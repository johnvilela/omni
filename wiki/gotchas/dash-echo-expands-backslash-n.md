---
tags: [shell, ci, tests, portability]
---

Fake `#!/bin/sh` scripts written by tests (e.g. the fake `claude` binary) run under **dash** on Ubuntu CI but **bash** on Arch dev machines. dash's builtin `echo` interprets `\n` as a real newline; bash's does not. So `echo '{"result":"line1\nline2"}'` emits valid JSON locally but a raw newline inside the string on CI → `claude: bad json output: invalid character '\n' in string`, and the failure looks flaky/environment-specific.

**Rule:** in test-generated shell scripts, never `echo` a string containing `\n` (or any backslash escape). Use `printf '%s\n' '<string>'` — `%s` never interprets escapes in the argument, identical on dash and bash.

Hit in `server/plan_test.go` `TestPlanCronDone` (PR #1, fixed in f55744d).
