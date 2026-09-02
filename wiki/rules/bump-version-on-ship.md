---
tags: [release, versioning, workflow]
---

# Bump the version on every feature or bugfix

Owner's standing rule (2026-09-02): every change that adds a feature or fixes a bug bumps `const Version` in `version/version.go` as part of the same change — minor for features, patch for fixes.

It's the one omni-wide hand-bumped version shared by cli and server (see [[api]]); CI enforces a PR version gate and auto-releases on master push, so an unbumped version blocks the PR.
