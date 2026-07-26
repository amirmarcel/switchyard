# Switchyard — Core Hardening Slice (Option A) — design spec

**Purpose.** Close the live and reachable-now findings from
`docs/reviews/2026-07-25-scheduler-review.md` before adding the second policy. This slice
makes the core *honest* — it fixes an invariant that is actively violated on the shipped
scenario (F3), closes a starvation bug reachable by event ordering (F2), removes a
knowingly-broken checker (G1), and adds the test that proves the load-bearing dual-backend
thesis (G4). It adds no features. Affinity comes after this.

Hand to Claude Code. Read the review doc, `scheduler-seam-spec.md`, `benchmark-harness-spec.md`,
CLAUDE.md, and the existing `docs/adr/`. **Design decisions below are fixed.** This slice
produces a new ADR for F3.

**In scope:** F2, F3 (+ its ADR), G1, G4. **Not in scope:** F1, F5 (chaos-prep,
see `docs/known-issues.md`), and everything else in known-issues. Do not touch them.

---

## F2 — close the ordering-reachable starvation (scheduler core)

**The bug (confirmed in review):** `admissible` returns true unconditionally when no
worker is registered yet, and admission is never revisited when a worker later joins. A job
whose needs exceed the eventual pool sits at the head forever and starves the whole queue
behind it — the exact bug ADR-0001 was written to eliminate, resurrected by event ordering.

**The decision (fixed):** re-run admission over the pending set when `WorkerRegistered`
fires. A job that is now provably unplaceable (needs exceed max worker capacity) must be
rejected at that point, exactly as if it had been submitted after the worker existed. This
touches only the scheduler core — no policy change, no backfill.

Requirements:
- On `WorkerRegistered`, after updating the worker pool, re-evaluate admission for all
  currently-pending jobs; reject any now-unplaceable ones (emit `Reject` decisions,
  consistent with ADR-0001).
- Keep the existing admission-at-submission behavior; this adds a re-check, doesn't replace.
- Decide and document the "no workers registered yet" case: either keep provisional
  admission (now safe, because the WorkerRegistered re-check will catch it) or reject
  eagerly. Provisional-admit-then-recheck is fine and simpler; state which you chose.
- Determinism preserved (ordered iteration over the pending set).

**Test (must fail before, pass after):** the review's exact reproduction — submit an
oversized job, THEN register a worker too small for it, THEN submit normal jobs; assert the
oversized job is rejected and the normal jobs dispatch (not starved).

---

## F3 — work-conservation: declare it, label it, check it

**The bug (confirmed in review):** FIFO head-of-line-blocks, holding jobs while capacity
exists — 164 times (46% of holds) on the shipped burst scenario. CLAUDE.md calls
work-conservation non-negotiable and says "an unexplained hold is a bug." Nothing checks it,
and FIFO's holds are mislabeled `Factors: {"no_capacity": 1}` even when capacity exists but
is merely too small for the head job. This is the same shape as the two bugs that already
shipped: the one invariant being violated is the one the harness can't see.

Three parts, all required:

**(a) An ADR declaring FIFO's non-work-conservation.** Write
`docs/adr/0004-fifo-non-work-conservation.md`: FIFO is deliberately strict head-of-line and
therefore *non-work-conserving by design* (it may hold a job while a smaller queued job
would fit a free worker). This is the explicit reservation the conditional invariant
requires — FIFO reserves capacity for the head job. Hold reasons are enumerated. Backfill
remains deferred (per ADR-0001). This closes the gap the review names: ADR-0001 reconciled
strict FIFO with *starvation* but never with *work-conservation*.

**(b) Honest hold labels.** FIFO's `Hold` `Factors` must distinguish the two cases:
- truly no free capacity anywhere → e.g. `{"no_capacity": 1}`
- head job doesn't fit, capacity reserved for it (head-of-line) → e.g.
  `{"head_of_line_reserved": 1}` (or similar)
So the decision log can actually be audited for work-conservation — which is the point of
`Factors`.

**(c) A work-conservation checker in the harness.** Add to `bench/invariants.go` a check
that, for each `Hold`, verifies it is *justified* per the conditional invariant: a hold is
legal only if it's genuinely no-capacity OR it's a declared reservation (head-of-line for
FIFO). An *unexplained* hold — capacity exists, a queued job fits, and the hold is not a
declared reservation — is a violation. FIFO's head-of-line holds are legal under this
checker (they're declared reservations); the checker exists to catch a *future* policy that
holds without declaring why.

**Tests:** (1) FIFO on a heterogeneous scenario passes the work-conservation checker (its
holds are all declared head-of-line reservations); (2) a deliberately-bad test policy that
holds while capacity exists *without* declaring reservation is flagged by the checker.

---

## G1 — delete the known-broken pairwise capacity checker

**The bug (confirmed in review):** `scheduler/scheduler_test.go` still contains the
pairwise interval-overlap capacity check that `bench/invariants.go` explicitly fixed and
that `bench/invariants_test.go` has regression tests against. It's a false-positive
generator that passes only vacuously (no scheduler-package scenario places two jobs on one
worker). A checker the codebase's own comments call wrong should not remain in the tree.

**The decision (fixed):** remove the duplicate broken checker from `scheduler_test.go`. If
the scheduler package needs a capacity assertion in tests, call the corrected
`bench`-package checker (or a shared one) — do not maintain a second, wrong implementation.
While here, add at least one scheduler-level scenario that actually bin-packs (two jobs
sharing one worker within capacity), so the partial-capacity arithmetic in `enactDispatch`
and FIFO's running-free decrement are exercised directly, not only through bench.

---

## G4 — the sim-vs-real decision-log comparison (the load-bearing test)

**The gap (confirmed in review):** the seam spec's acceptance bar is explicit — the two
backends "must match on which job went where under FIFO." Today `scheduler_test.go` and
`executor/driver_test.go` each independently assert dispatched-exactly-once, but **no test
compares the two backends' job→worker mapping.** The one criterion that actually proves the
dual-backend thesis is unasserted. The review calls this the single highest-value test in
the repo.

**The decision (fixed):** add a test that runs the *same* workload (same jobs, workers,
seed) through both the sim backend and the real (fake-executor) backend under FIFO, and
asserts the two decision logs agree on job→worker placement (not on timing). This proves
"same scheduler code, both backends" is real, not asserted.

---

## Definition of "done"

- [ ] F2: `WorkerRegistered` re-runs admission over pending; oversized-then-small-worker
      reproduction is rejected + normals dispatch; test fails-before/passes-after.
- [ ] F3a: ADR-0004 declares FIFO non-work-conservation.
- [ ] F3b: FIFO hold `Factors` distinguish no-capacity vs head-of-line-reserved.
- [ ] F3c: work-conservation checker in `bench/invariants.go`; FIFO passes it; a bad test
      policy is flagged.
- [ ] G1: broken pairwise checker removed from `scheduler_test.go`; a real bin-packing
      scenario added at the scheduler level.
- [ ] G4: sim-vs-real job→worker comparison test under FIFO.
- [ ] All existing tests still green under `-race`; determinism preserved.
- [ ] Build-log entry referencing F2/F3/G1/G4 by ID and the review doc.

## Explicitly NOT in this slice

F1, F5 (chaos-prep), F4, F6–F14 (see `docs/known-issues.md`), any second policy, backfill,
the real Docker executor, Prometheus/OTel, CLI. Do not start affinity — that is the next
slice, after this one merges.

## First task to hand Claude Code

> Read `docs/reviews/2026-07-25-scheduler-review.md`, `docs/known-issues.md`, and
> `docs/design/core-hardening-spec.md`. Implement the Option A hardening slice: F2, F3
> (+ ADR-0004), G1, G4 as specified — nothing else. Do NOT touch F1, F5, or any other
> known-issue. For F2, re-run admission on `WorkerRegistered` and reject now-unplaceable
> pending jobs; add the oversized-then-small-worker regression test. For F3, write ADR-0004
> declaring FIFO non-work-conservation, make FIFO's hold Factors distinguish no-capacity
> from head-of-line-reserved, add a work-conservation checker to bench/invariants.go, and
> test that FIFO passes it while a deliberately-bad hold-without-reserving policy is
> flagged. For G1, remove the broken pairwise capacity checker from scheduler_test.go and
> add a real bin-packing scheduler scenario. For G4, add a test comparing the sim and real
> backends' job→worker decision logs under FIFO on the same seeded workload. All existing
> tests must stay green under -race; determinism preserved. Report what you changed per
> finding ID.
