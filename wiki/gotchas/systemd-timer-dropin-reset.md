---
tags: [systemd, guardian, timer, drop-in]
---

# systemd timer drop-in empty reset wipes OnActiveSec

`omni-dev guardian interval <dur>` writes a drop-in `override.conf` for `omni-dev-guardian.timer`. An empty assignment like `OnUnitActiveSec=` resets **all** `On*Sec`/`OnCalendar` clauses in the unit — including the base unit's `OnActiveSec=2min` bootstrap, not just `OnUnitActiveSec`.

With only `OnUnitActiveSec` left, the timer never fires after a restart/daemon-reload: `OnUnitActiveSec` schedules relative to the service's last activation, which never happens. `systemctl --user status` shows `active (elapsed)` with `Trigger: n/a`, and `systemctl show -p TimersMonotonic` shows `next_elapse=0`. The guardian silently stops running — stale red alerts in `guardian.json` never clear, and `omni doctor` keeps reporting them even after the underlying issue (a transient telegram network outage) recovered.

Fix (cli/main.go `runGuardianInterval`, shipped v0.6.1): the drop-in must re-add the bootstrap after the reset:

```
[Timer]
OnUnitActiveSec=
OnActiveSec=2min
OnUnitActiveSec=<dur>
```

Rule of thumb: any drop-in that resets a systemd list-valued option must re-state everything the base unit set for that whole option family.

## Incident that surfaced it

Diagnosed live 2026-09-02 when `omni doctor` kept reporting a telegram alert two hours after a transient network outage (15:22–15:40, both IPv4 and IPv6 to `api.telegram.org` timed out and recovered on their own) had already resolved — the guardian's red alert from 15:35 never cleared because its timer had gone dead per the mechanism above. Fixed and shipped as commit `f732804` (v0.6.1); the live drop-in was also hand-repaired and the timer restarted to clear the stale alert immediately. Full session: [[sessions/bd950550-a3ff-4fc0-9b65-9e07ab326e4a]].

Also surfaced during that same diagnosis, unrelated to the timer bug: an old guardian binary (predating a since-shipped journal-token-redaction) had printed the raw bot token into a journal line at the incident timestamp; the currently installed binary masks it as `<token>`. Local journal only, not a live exposure — worth a BotFather token rotation only if that log line escaped further.

Related: the guardian's once-per-incident alert model lives in guardian/main.go (`guardian.json` state, recovery notices).