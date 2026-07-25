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