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

### 2026-07-25 — Core hardening slice (Option A): F2, F3, G1, G4

- **Task handed off:** close four findings from
  `docs/reviews/2026-07-25-opus-code-review.md` per
  `docs/design/core-hardening-spec.md`, nothing else — explicitly not F1, F5, or
  any other `docs/known-issues.md` item.
- **What came back:**
  - **F2** — `Scheduler.apply` now returns `[]api.Decision` instead of a single
    `*api.Decision`; `WorkerRegistered` calls a new `rejectUnplaceablePending`
    that re-runs `admissible` over every still-pending job in submission order
    and rejects any now provably unplaceable. Provisional admission (no
    worker registered yet) is kept, per the spec's stated preference, since
    the re-check now closes the gap. Regression test
    `TestWorkerRegisteredRejectsNowUnplaceablePending` reproduces the
    review's exact repro; confirmed it fails against the pre-fix
    `scheduler.go` (0 rejects, normals starved) and passes after.
  - **F3** — `docs/adr/0004-fifo-non-work-conservation.md` declares FIFO's
    head-of-line hold as the invariant's required explicit reservation.
    `FIFO.Schedule`'s `Hold` now calls `holdFactor` to label
    `no_capacity` vs `head_of_line_reserved`. `bench/invariants.go` gained
    `checkWorkConservation`, wired into `CheckInvariants`, which reconstructs
    free capacity per worker at each Hold's timestamp and flags a Hold whose
    `Factors` claim only `no_capacity` when a still-pending job actually
    fit somewhere. Tested both directions: FIFO on the shipped burst
    scenario passes cleanly, and a deliberately-bad test policy
    (`badHoldPolicy`) that mislabels a fitting hold is flagged.
  - **G1** — deleted `scheduler_test.go`'s pairwise-overlap
    `assertNoCapacityViolation` and its duplicate `interval` type. Exported
    `bench.CheckCapacityInvariant` (previously unexported) so
    `TestCapacityInvariant` calls the one corrected implementation instead of
    maintaining a second. Added a bin-packing scenario (two 400-CPU jobs
    against one 1000-CPU worker, both fit concurrently) to `scenarios()`,
    which every existing scenario-driven test now also exercises.
  - **G4** — new `executor/crossbackend_test.go`:
    `TestSimAndRealAgreeOnPlacementUnderFIFO` drives identical jobs/workers
    through both the sim and real (fake-executor) backends under FIFO and
    asserts the job→worker maps match exactly, per the seam spec's
    acceptance bar.
- **What needed correction:** the first cross-backend test used 20 jobs
  against 3 workers (mirroring `executor/driver_test.go`'s existing shape) and
  failed intermittently — once jobs have to wait for a worker to free up,
  which worker completes first is a real wall-clock race, not scheduling
  logic, so placement can legitimately diverge from the sim's instantaneous
  logical time without either backend having a bug. Fixed by sizing the test
  so every job dispatches immediately on submission (`numJobs == numWorkers`,
  each job needs a whole worker) — placement is then decided purely by
  submission/worker order in both backends, which is what the seam spec's
  bar is actually about.
- **Decision / outcome:** all four findings closed as specified; F1, F5, and
  everything else in `docs/known-issues.md` left untouched. `go vet`, `gofmt
  -l`, and `go test -race ./...` (including `-count=3`) all clean.
- **Artifacts:** ADR-0004; new tests in `scheduler/scheduler_test.go`,
  `bench/invariants_test.go`, `executor/crossbackend_test.go`; no new
  packages.

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

### 2026-07-24 — Gate caught permanent-starvation invariant violation in FIFO

- **Task handed off:** vertical slice (prior commit); pushed through no-mistakes gate.
- **What the gate found:** FIFO's head-of-line `break` (fifo.go:34) permanently starves
  the whole queue behind any job that can never fit (Needs > every worker), violating
  the non-negotiable "every job eventually scheduled" invariant. Concrete repro in the
  finding. The slice's own tests missed it — uniform job/worker sizing never exercised
  the path.
- **What needed correction / design call:** two problems separated — (1) impossible job
  is an *admission* concern, reject at submit; (2) head-of-line blocking kept strict for
  v1 (backfill deferred as non-goal). Human made the call; agent implements.
- **Decision / outcome:** admission check + ADR, keep strict FIFO, add non-uniform tests
  that fail-then-pass. Verified the finding against fifo.go before acting.
- **Artifacts:** ADR (admission), fix commit, new tests. Gate run aborted and re-pushed.

### 2026-07-25 — Benchmark harness slice (bench/)

- **Task handed off:** implement the benchmark harness from the spec — seeded burst
  workload, sim-backend runner, exact p50/p95/p99 queue-delay percentiles, ≥5-run
  median+spread methodology, JSON+stdout output, invariant checking. FIFO only, sim only,
  no Prometheus/CLI/second policy (all deferred per spec).
- **What came back:** the full bench/ package + ADR-0002 (exact-percentiles-from-raw-
  samples over bucketed histograms; replay-not-regenerate). Percentile math is nearest-
  rank over sorted raw samples; median takes the middle run whole (not field-averaged);
  determinism asserted as a precondition.
- **Bug caught during implementation:** the first capacity-invariant checker used pairwise
  interval-overlap, which over-counts when jobs bin-pack (several jobs legitimately sharing
  one worker within capacity) — same root cause inflated WorkerUtilization above 1.0. Agent
  caught it, switched to a start-point sweep-line (sum active intervals at each interval
  start), utilization settled to ~0.86.
- **What needed correction / verification:** review found the fix had no regression test
  pinning it — the same gap that let the earlier FIFO starvation bug ship. Had the agent add
  three hand-constructed capacity tests (bin-packing within capacity, over capacity, adjacent-
  interval boundary), verified fail-then-pass against a reverted pairwise checker. The agent
  also corrected a wrong expectation I'd given it: the adjacent-boundary test does NOT
  differentiate old vs new (both use strict `<` at edges) — it kept the test honest and said
  so rather than gaming the assertion to match the prediction.
- **Decision / outcome:** committed. Capacity fix now locked by regression tests. Stale doc
  comment on the checker also fixed (CLAUDE.md "keep comments current" rule).
- **Artifacts:** squash-merged to main as a single `feat(bench)` commit (PR #5); ADR-0002
  (exact percentiles, replay-not-regenerate). Tests green under -race ×5.
