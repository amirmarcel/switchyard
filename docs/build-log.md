# Build log

A running record of how Switchyard was built with an AI coding agent (Claude Code)
under human direction. Each entry captures a handoff, what came back, what needed
correction, and the decision made — the orchestration record, not a changelog.

This exists because on this project the human's work is direction, review, and
judgment, not typing. This log is where that work is visible: what was specified,
where the agent drifted, and how it was corrected.

## How to use

- One entry per meaningful handoff (a slice, a subsystem, a non-trivial fix).
- Write it when you review the agent's output, not later.
- Keep it honest — the corrections and dead ends are the point, not just the wins.
- Link the PR and any ADR the entry produced.

---

## Entry template

### YYYY-MM-DD — <short title>

- **Task handed off:** what you asked the agent to do, and the scope boundary you set.
- **What came back:** what it produced on the first pass.
- **What needed correction:** where it drifted from the spec, missed an invariant,
  over-scoped, or got a design call wrong — and how you caught it (review, failing
  test, invariant check).
- **Decision / outcome:** what you accepted, changed, or rejected, and why.
- **Artifacts:** PR link, ADR(s), tests added.

---

## Entries

<!-- newest first -->

### 2026-07-24 — Doc comments across api/executor/scheduler/simulation

- **Task handed off:** Add Go doc comments following the CLAUDE.md rules, with
  judgment over which files need a package doc, a file header, or nothing. Explicitly
  told it not to comment mechanically; asked it to report its choices.
- **What came back:** One package doc per package (api/types.go, scheduler/scheduler.go,
  simulation/clock.go, executor/clock.go — already present, updated to cross-reference
  the seam spec and sibling packages). One file header added (scheduler/scheduler_test.go).
  Everything else deliberately left alone with per-file reasoning.
- **What needed correction:** Nothing. Verified comments-only: `git diff --stat` showed
  5 files touched; grep for non-comment/non-blank changed lines returned empty.
- **Decision / outcome:** Accepted as-is. The point worth recording is the agent
  exercised the delegated judgment correctly — skipped fifo.go, state.go, events.go,
  interfaces.go because filename + existing type docs already carry the role, rather
  than commenting every file. Restraint was the right call.
- **Artifacts:** commit `docs: add package and file doc comments`. No ADR. No tests changed.