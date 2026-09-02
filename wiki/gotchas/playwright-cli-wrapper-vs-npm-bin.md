---
tags: [playwright, agent-mode, dev-machine, browser]
---

# playwright-cli: omarchy wrapper vs the npm bin

The agent-mode seed (CLAUDE.md/AGENTS.md in the agent workspace, `agentSeed` in
server/agent.go) tells the agent to run `playwright-cli attach --cdp=…`,
`goto`, `snapshot`, `click`, etc. That spelling is correct for the **target
machine**, where `npm install -g playwright` exposes the package's real
`playwright-cli` bin directly.

On the **dev machine** (omarchy), `~/.local/bin/playwright-cli` is an omarchy
npx wrapper that resolves the package's `playwright` bin first — so
`playwright-cli <cmd>` actually runs `npx playwright <cmd>` (the generic
test-runner CLI), and the agent driving commands need an extra `cli` prefix:
`playwright-cli cli attach --cdp=…`, `playwright-cli cli goto …`.

**Why it matters:** testing `/agent` browser tasks on the dev machine will fail
with "unknown command" while the same seed works fine on the target. Verified
2026-09-01 against playwright 1.62.1 (`playwright-cli cli --help` shows the
driving commands: open/attach/goto/click/fill/snapshot/find/eval, `attach
--cdp` confirmed).

Fix options if dev testing matters: edit the dev workspace's CLAUDE.md by hand
(it is seeded once and never overwritten), or symlink the real bin ahead of the
wrapper in PATH.

Related: [[api]] (agent-mode bullet), [[install]] (agent dependencies block).
