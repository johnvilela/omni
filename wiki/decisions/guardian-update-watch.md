---
tags: [guardian, updates, config]
---

# Guardian update watch (v0.12.0)

The guardian (see [[install]]) can alert when a watched companion tool has a newer GitHub release — e.g. a new memoria version.

- **Opt-in, owner-curated**: config.yaml `update_repos: [johnvilela/memoria, …]` lists GitHub `owner/name` repos. Empty/absent = check off ("not all packages should be checked" — the owner names them; no hardcoded list).
- **Throttled**: runs at most every 6h even though the guardian fires every 2min. The schedule is the mtime of `~/.local/share/<app>/updates.stamp` — deliberately NOT a guardian.json key, because `omni doctor` and `omni guardian` render every guardian.json key as an active alert.
- **Version discovery**: binary named like the repo basename, parsed from `<bin> --version`; omni itself is special-cased to the compiled-in `version.Version` (the CLI has no `--version` command). Not-installed tools are silently skipped.
- **Alerting**: reuses the transition machinery — one `⚠ updates: memoria 0.16.0 → v0.17.0 — rerun scripts/install.sh` alert per incident, `✅ recovered` once updated. The standing `updates` red key is carried across throttled runs in main(), since `transitions()` drops keys with no result.
- **No fake verdicts**: any failed release lookup makes the whole check indeterminate (no result added, previous state kept) — a GitHub hiccup must not send a false "recovered". `semverLess` compares numerically (v-prefix and `-suffix` stripped), so a source build ahead of the latest release never alerts.
- Test override: `OMNI_GITHUB_API` env points `latestRelease` at a fake server, mirroring `OMNI_TELEGRAM_API`.
