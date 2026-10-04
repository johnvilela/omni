---
tags: [docker, container, systemd, workflow]
---

# Container mode has no systemd

`OMNI_CONTAINER=1` (set by the docker image) means there is no `systemctl`, `journalctl` or `systemd-run`. Never call them directly from new code — go through the container-aware helpers and give each new helper both branches:

- guardian: `restartServer`, `updateHint`, `loopInterval`
- server: `kickGuardian`, `opsRestart`/`opsLogs` branches, `restartAIMemory`
- cli: `installScript`, `serverRestartFix`, `processCheck`, `containerGuardianLine`

Test the container branch under `t.Setenv("OMNI_CONTAINER", "1")` with PATH shims for `pgrep`/`pkill` (never let a test reach the real `pkill`), and pin `t.Setenv("OMNI_CONTAINER", "")` in host-mode harnesses so the suite also passes inside the image.

In docker "restart X" means "kill X": `scripts/docker-entrypoint.sh` respawns every process on any exit code. Vendor clis may be host binaries bind-mounted read-only at `/opt/host/<name>/<name>` — never assume they are writable or npm-managed. `server/docker_contract_test.go` greps `Dockerfile` + the entrypoint for the contract.
