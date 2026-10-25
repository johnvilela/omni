---
tags: [tasks, cli, docker, security]
---

# Pin plugin, vendor and image versions; require checksums (SEC-10)

**Status:** DEFERRED — 2026-10-25
**Trigger:** a plugin or vendor cli release breaks or a publisher is compromised.
**Branch / PR:** —

## Request

The 2026-10-25 security evaluation (medium): `cli/plugins.go` tolerates a missing checksums.txt and executes the downloaded binary to read its manifest before validating it; `scripts/docker-entrypoint.sh` npm-installs unpinned vendor packages and plugins at every start; the Dockerfile and release workflows use mutable tags.

## Notes

- Refuse plugins without checksums; validate the manifest from the archive before any execution.
- `OMNI_VENDOR_INSTALL` entries with `@version`; image digests and action commit pins where practical.

## Done when

- [ ] A plugin release without matching checksums is refused.
- [ ] Restarting a pinned deployment never changes executable code silently.
