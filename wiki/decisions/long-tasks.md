---
tags: [tasks, checkpoints, agents, concurrency]
---

# Long tasks with checkpoints (v0.15.0)

Omni executes long multi-step jobs (bank-statement → pecunia setup, LinkedIn job hunts) without context loss or hallucinated progress: a deterministic Go loop (server/task.go) drives **fresh-context** one-shot agent runs — the [[api]] `fireCron` agent pattern — against a markdown checkpoint the agents themselves maintain. No step depends on model memory; a crash resumes from the checkpoint.

## Shape

- **State split**: sqlite `tasks` table (id, goal, status, step, note) holds orchestration only; checkpoint CONTENT lives at `agentDir()/tasks/<id>/task.md`, written by the step agents with their native file tools. The server never parses task.md.
- **Control protocol**: each step run must END its reply with one control line, parsed from the **last non-empty line only** (`parseControl` — scanning the whole reply false-positives on replies quoting the contract): `CONTINUE <note>` | `DONE <summary>` | `BLOCKED <question>` | `FANOUT <n>`. Step replies get NO TOOL processing — a step agent structurally cannot start sub-tasks; only the DONE summary passes `applySendFile`.
- **Loop rails**: statuses running|paused|blocked|done|failed|cancelled; `maxTaskSteps=30` persisted cap; CLI error retries the iteration once (2 consecutive → failed); missing control line = strike, bumped as a step (2 consecutive → failed). Strike/err counters are loop-local; the persisted step cap still bounds a crash-reset runaway.
- **Fan-out**: step writes `items/1..n.md` briefs, replies `FANOUT n` (clamped to 8 declared); workers run under `Server.agentSem` (**global cap 4 concurrent agent runs**, steps + workers, all tasks — the sem is held only around a CLI run, never across orchestration). Workers write `results/<k>.md` and never touch task.md; a worker that errors/forgets gets its results file written by the server (error text / its reply). items/ removed after workers finish; results/ removed after the merge iteration ran (merge = next step when results/ non-empty — its prompt says merging IS the step). A cancelled worker leaves no results file.
- **Steering**, one mechanism for BLOCKED answers and mid-flight notes: `/task #<id> <text>` appends `## Owner note (<ts>)` to task.md (O_APPEND|O_CREATE) and resumes blocked/paused; step prompt says owner notes override the plan. `/task <goal>` starts (explicit `#` disambiguates goals starting with a number), `/task #id` shows status.
- **Boot resume**: `resumeTasks()` in main.go after `replayQueue()` restarts loops of `running` tasks. Re-running an interrupted step against the checkpoint is safe; planning (step 0) overwrites task.md.
- **Surface**: `/tasks` listing with `task-pause:/task-resume:/task-cancel:<id>` callbacks (routed in `gatedCallback` above the bare-uuid fallthrough, Edit:true re-render like cron delete); pin line gains `· K tasks` (running+blocked) and full mode one row per live task with live worker counts (`Server.taskWorkers`); `TOOL:task_start {"goal"}` from chat (in `defaultApprovalTools` — spawns full-permission agents) via `taskPrompt()` injected beside cronPrompt/filePrompt, and from agent sessions/cron via `applyAgentTools` (generalized `applySendFile`, deliberately ungated there — the vendor CLI already has full permissions). Workspace seed grew `taskContract`; `ensureAgentDir` migration loop generalized to marker+section pairs.
- Pecunia integration needs **zero omni code**: step agents run un-bare, so registering pecunia's MCP in the vendor CLI user config is enough. Goals must carry every detail — task agents see nothing else (the contracts say so explicitly).

## Gotchas hit

- Pause sets status BEFORE cancelling the loop ctx, so a killed in-flight run doesn't count as an error; a resume tapped within ms of pause can hit the dying loop's one-loop-per-task guard and no-op (`ponytail:` comment in runTaskLoop — tap resume again; done-channel handshake if it ever bites).
- Chat-prompt-overhead tests (`TestChatAnswerBudget`, `TestCompaction`) hardcode the injected tool sections — any new prompt section must be added to their `overhead` expression.
- Test fakes: branch the fake claude on the **prompt text** (`case "$2" in *"PLANNING step"*...`) instead of a call counter — concurrent workers race a counter file. And the test PATH holds only the fake bin: a fake that shells out (grep etc.) must restore `PATH=/usr/bin:/bin` itself, or it fails with "command not found" swallowed by `2>/dev/null`.
- Lock order extended: pinMu → (qmu | s.mu | taskMu | store), never reversed.

Related: [[decisions/background-session-queue]], [[decisions/chat-tool-approval-gate]], [[api]]