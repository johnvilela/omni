# Tasks

Backlog of deferred work. One file per task, `NNNN-short-slug.md`, numbered in the order they arrive. Nothing else goes in this folder.

## File format

```markdown
---
tags: [tasks, <area>, <area>]
---

# <Task title>

**Status:** NEW | DEFERRED | IN PROGRESS — <date recorded>
**Trigger:** what un-defers it (DEFERRED only) — pick the task up when this fires, not before
**Branch / PR:** `feat/<slug>` (#NN) — once work starts

## Request

What was asked, or the gap observed, with the code that is involved (`package/file.go`, symbol names).

## Notes

Pointers that save a re-investigation: ai-memory pages (`concepts/…`, `gotchas/…`), related code markers, prior attempts.

## Done when

Acceptance list. Checked off in the PR.
```

## Lifecycle

- This folder holds only work that is still to be done. Work that starts right away gets no task file, and nothing here describes what already shipped.
- A task is created when a request or a known gap is parked for later. `Status` moves to IN PROGRESS when the branch is cut.
- Delete the file when the work is merged into `master`. The how-it-works of what shipped belongs in ai-memory (`concepts/…`, `decisions/…`), not here.
- Rules discovered while building go to [`../rules/`](../rules/), not into the task file.
