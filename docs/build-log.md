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

### 2026-08-01 — Lease fencing on completion/failure events (F1, F5)

- **Task handed off:** implement lease fencing so at-most-once holds by
  mechanism, not by luck (`docs/known-issues.md` F1): `api.JobCompleted`/
  `api.JobFailed` gain a lease-token field; both executors propagate the
  dispatch's `LeaseID` into the completion/failure event; the scheduler
  rejects any completion/failure whose lease doesn't match the assignment's
  current lease, logging the rejection as fenced and leaving the current
  assignment untouched. Lease generation had to stay deterministic
  (jobID + monotonic counter, no timestamps/UUIDs/unseeded randomness) to
  keep the byte-identical decision-log invariant intact. Coupled fix: F5's
  at-most-once checker (`bench/invariants.go`) counted dispatches, so a
  legitimate retry read as a violation — fix it to count accepted
  completions instead. Explicitly out of scope: any chaos/failure-injection
  harness, F6 (retries), F8 (cancellation), and README edits (proposal only).
- **What came back:** `api.Decision` and both event types gained `LeaseID`.
  `enactDispatch` now returns the dispatched `Decision` with `LeaseID` set
  (previously `Assignment.LeaseID` was generated but never read by
  anything — grep confirmed exactly one occurrence, the write); `Handle`
  threads the returned, lease-carrying decision into both the executor call
  and the log. Both executors (`executor.FakeExecutor`,
  `simulation.Executor`) copy `Decision.LeaseID` into the `JobCompleted`
  they construct. The scheduler's `JobCompleted`/`JobFailed` handling now
  distinguishes three cases: no assignment at all (silently ignored, as
  before), assignment exists but worker/lease mismatch (fenced — new
  `api.Fenced` outcome, logged, rejected, assignment untouched), or a match
  (accepted — `JobCompleted` now also logs a new `api.Completed` outcome).
  `bench.CheckInvariants`' at-most-once check now counts `Completed`
  decisions per job instead of `Dispatch` decisions; starvation stays on
  dispatch/reject counts. ADR-0005 documents the mechanism and why a
  per-dispatch token is required over worker-ID matching (worker-ID fails
  exactly when a job is reassigned back onto the *same* worker after a
  partition — the common case, not an edge case).
- **What needed correction:** nothing structural — the design was fully
  specified up front (this was a directed implementation, not an open
  design task). One judgment call made during implementation: whether a
  job with no running assignment at all (`!ok`) should also emit a `Fenced`
  decision for symmetry. Decided no — that case has no assignment to fence
  against and matches the event's pre-existing "stale/duplicate, nothing to
  compare" semantics; only a *mismatched* assignment is fencing's concern.
- **Decision / outcome:** accepted as specified. Verified fail-before/
  pass-after for both fixes by temporarily reverting to the old logic
  (worker-ID-only match; dispatch-count-based checker) and confirming the
  new regression tests fail, then restoring and confirming they pass:
  `TestLeaseFencingRejectsStaleCompletion` (scheduler) reproduces the
  review's exact repro — dispatch under L1, `JobFailed` releases and
  retries under L2, L1's late completion arrives and must be fenced while
  L2's genuine completion is accepted — and fails against a worker-ID-only
  revert (0 fenced decisions). `TestAtMostOnceCheckerIgnoresLegitimateRetry`
  / `TestAtMostOnceCheckerFlagsTwoAcceptedCompletions` (bench) fail against
  a dispatch-count revert and pass against the fix.
  `TestNormalCompletionAcceptedUnderMatchingLease` pins the no-regression
  case (ordinary completion, no reassignment). `go build`, `go vet`, and
  `go test -race ./...` all clean; existing `TestDeterminism` (unchanged)
  continues to assert byte-identical logs across reruns, now over logs that
  include lease-bearing `Dispatch`/`Completed` decisions.
- **Artifacts:** ADR-0005; `api/events.go`, `api/types.go`,
  `scheduler/scheduler.go`, `executor/fake.go`, `simulation/executor.go`,
  `bench/invariants.go`; new tests in `scheduler/scheduler_test.go` and
  `bench/invariants_test.go`; `docs/known-issues.md` F1/F5 removed
  (resolved). README wording changes proposed, not applied — see the
  reviewer's response for the exact lines.

### 2026-07-25 — checkWorkConservation perf: O(holds × log length) → O(log length)

- **Task handed off:** `TestPriorityAffinityPassesWorkConservationChecker`
  took ~23s (100s+ under `-race -count=5`) while FIFO's structurally-similar
  test took 0.08s. Profiling pinned ~42% cumulative to
  `checkWorkConservation` → `freeCapacityAt`. Root cause: `freeCapacityAt`
  rescanned the whole log per Hold, so the checker was O(holds × log
  length); FIFO has few holds (breaks on the first miss), PriorityAffinity
  is work-conserving and `continue`s past every miss, so it legitimately
  produces tens of thousands of holds on the burst scenario (30,803, per a
  quick count) — the quadratic bites hard. Asked for a single
  timestamp-ordered sweep with a running tally (matching
  `CheckCapacityInvariant`'s existing start-point-sweep technique), a
  `testing.Short()` guard on the expensive test, and confirmation the
  checker's output doesn't change.
- **What came back:** `freeCapacityAt` deleted; `checkWorkConservation` now
  does one forward pass over the log, tracking `usedCap` incrementally and
  retiring expired dispatches from a FIFO queue (`active`). This works
  because every dispatch in a `Workload` shares one fixed `JobDuration`, and
  `log.At` is non-decreasing in index order (the scheduler appends decisions
  in real event-processing order) — so dispatch end times are added in
  non-decreasing order too, making a plain queue (not a heap) sufficient:
  the oldest-added entry always expires first. `TestPriorityAffinityPassesWorkConservationChecker`
  gained a `testing.Short()` skip. Verified the checker's output is
  unchanged by rerunning all four checker-behavior tests (FIFO pass,
  bad-hold-policy positive control, the boundary-tie regression test, and
  the affinity pass) — all still pass with the same semantics, just faster.
- **What needed correction:** nothing — this was scoped tightly to the one
  function the profiling identified; `CheckCapacityInvariant` (a separate,
  already-adequate O(dispatches²)-per-worker check, not implicated by the
  profile) was left untouched.
- **Decision / outcome:** accepted. Timing:
  `TestPriorityAffinityPassesWorkConservationChecker` alone: ~23s → ~2.2s
  (no `-race`), and ~11s per run under `-race` (previously "100s+" for 5
  runs combined, i.e. ~20s/run). `go test -short ./...`: 9.5s total (the
  test now skips in that mode). Full suite `go test -race -count=5 ./...`:
  clean, ~4m8s. Remaining cost in the un-skipped test is the fit-check inner
  loop (holds × jobs × workers ≈ 49M lookups for this scenario's 30,803
  holds), which the profiling didn't flag and which is out of scope here.
- **Artifacts:** `bench/invariants.go` (`freeCapacityAt` removed,
  `checkWorkConservation` rewritten as a sweep); `bench/invariants_test.go`
  (`testing.Short()` guard). No new tests — behavior is unchanged by
  design, pinned by the existing four checker tests.

### 2026-07-25 — Priority + cache-affinity candidate policy

- **Task handed off:** implement `PriorityAffinity` per
  `docs/design/candidate-policy-spec.md` and ADR-0003 — priority ordering
  plus warm-worker placement, work-conserving, pure/deterministic — and
  extend the burst scenario with cache keys + priorities to produce a real
  FIFO-vs-affinity comparison through the existing harness. Explicitly not
  aging, warmth decay, WFQ/DRF, the real executor, Prometheus, or a CLI; not
  F1/F5 or any other `docs/known-issues.md` item.
- **What came back:** `api.Job.CacheKeys`/`api.Worker.WarmCache` already
  existed in `api/types.go` (unused) — no model change needed there.
  `scheduler.go`'s `enactDispatch` gained `warmOnRun`: warm-forever-on-dispatch,
  merged/deduped/sorted for determinism. New `scheduler/priority_affinity.go`:
  sorts pending by (priority desc, submit asc, JobID), places each job on the
  free-capacity worker warmest on its `CacheKeys` (ties broken by worker ID),
  records `priority`/`cache_affinity`/`warm_keys_matched` in `Factors` on
  Dispatch. `bench/workload.go`'s `BurstParams` gained `CacheKeyPoolSize`/
  `CacheKeysPerJob`/`HighPriorityCount`/`HighPriority` (all zero-default, so
  every existing FIFO-only test is unaffected); `BurstScenario()` now carries
  a 16-key pool (2 keys/job, meaningful overlap over 200 jobs) and a 20-job
  high-priority subset. New `bench/compare_test.go` runs both policies
  through the ≥5-run harness on the identical scenario and logs the p99 delta
  and affinity hit-rate, asserting both are favorable and every invariant
  (including work-conservation) holds under both. Four required tests added:
  determinism, bounded no-starvation, warm-worker placement
  (`scheduler/priority_affinity_test.go`), and a work-conservation-checker
  pass on the burst scenario (`bench/invariants_test.go`).
- **What needed correction:** three issues, all self-caught before landing
  (running `TestPriorityAffinityPassesWorkConservationChecker` against the
  real burst scenario, not from review):
  1. The policy's `Hold` decisions initially included `"priority"` in
     `Factors` alongside `"no_capacity"`. `bench.checkWorkConservation`'s
     `declaresReservation` treats any Factors key other than `no_capacity`
     as a declared reservation (that's how it tells FIFO's legitimate
     head-of-line holds from a bug — ADR-0004) and would have silently
     stopped checking every hold for a prioritized job, exactly the case
     most worth checking. Fixed by keeping Hold `Factors` to
     `{"no_capacity": 1}` only; `priority` stays on Dispatch decisions where
     it's unambiguous.
  2. First real run of the checker against the burst scenario failed with
     hundreds of "unexplained hold" violations. Root-caused (not a policy
     bug) to `checkWorkConservation` itself: it had only ever been exercised
     against FIFO, whose holds are always the terminal decision of a
     `Schedule` call. `PriorityAffinity` is work-conserving and `continue`s
     past a miss, so a call can legitimately emit `Hold(job A), ...,
     Dispatch(job B)` — the checker, walking the log index-by-index, didn't
     yet know B's dispatch had already claimed the capacity it was asking
     about when checking A's hold. Fixed in `scheduler/priority_affinity.go`
     by emitting every Dispatch from a call before any Hold from the same
     call (log-ordering only — doesn't change which job goes where).
  3. Violations persisted after (1) and (2). Reconstructed the real
     scheduler state by hand (temporary debug prints comparing
     `s.FreeCapacity` inside `Schedule` against the checker's own
     `freeCapacityAt` reconstruction at the flagged instants) and found a
     second, independent bug: two separate `Schedule` calls can share the
     exact same logical instant (e.g. a job's completion and some other
     event both at `t`), and the earlier one genuinely runs *before* that
     completion is processed — the real scheduler still shows the
     completing job occupying its worker at that instant.
     `freeCapacityAt`'s strictly half-open window treated the boundary as
     already free for every decision at that `t`, flagging a hold that
     hadn't actually had its capacity yet. Fixed by making the window's
     upper bound inclusive for this specific reconstruction
     (`bench/invariants.go`); `CheckCapacityInvariant` keeps its existing
     half-open convention, since it only reasons about genuinely distinct
     instants. Verified against the real scheduler state, not just the
     checker's own output, before accepting the fix — the debug
     reconstruction and the live `s.FreeCapacity` agreed once corrected.
  All three documented in `docs/design/priority-affinity-notes.md`
  alongside the modeling calls that weren't corrections (warm-on-dispatch
  vs. warm-on-completion; no intra-batch warmth propagation).
- **Decision / outcome:** accepted as specified, plus the two
  `checkWorkConservation` fixes (findings 2 and 3 above — pre-existing
  checker gaps this policy was the first to exercise, not new scope).
  `go vet`, `gofmt -l`, and `go test -race ./...` all clean.
- **Artifacts:** `docs/design/priority-affinity-notes.md` (design note);
  `scheduler/priority_affinity.go` + `scheduler/priority_affinity_test.go`;
  `bench/compare_test.go`; extended `bench/workload.go`, `bench/scenario.go`;
  `bench/invariants.go` fix (boundary-tie reconstruction) +
  `bench/invariants_test.go` (new `TestPriorityAffinityPassesWorkConservationChecker`,
  `TestWorkConservationBoundaryTieNotFlagged`); ADR-0003 (already committed,
  policy choice).

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

### 2026-07-31 — README-accuracy audit and honesty corrections

- **Task handed off:** run an accuracy audit of README.md against the current code
  and docs/known-issues.md — find every place the README claims a capability that
  doesn't exist, isn't tested, or is contradicted by a known-issue. Report only,
  no edits, then apply a specified subset of corrections.
- **What the audit found:** seven present-tense claims for capabilities that don't
  exist in the code — real Docker executor ("Not a paper design", but go.mod has
  zero deps and only FakeExecutor exists), fault tolerance / failure injection (no
  chaos code at all), lease fencing (LeaseID written but never read — F1),
  cancellation of in-flight work (both Cancel() are no-ops — F8), retries/idempotency
  (no retry mechanism — F6), Prometheus/metrics (no metrics code), and benchmark
  warm-up discard (claimed in methodology, never implemented). Plus overstated claims:
  "hundreds of thousands of jobs" (blocked by the O(N²) snapshot, F10) and "invariants
  checked on every run" (only 2 of 7 invariants have automated checkers, F5). The
  accurate sections (determinism, the two policies, admission/capacity, benchmark
  methodology) were confirmed correct and tested.
- **What needed correction / decisions made:** each false claim was tagged
  soften-the-doc vs. build-the-code. Softened for now: Docker executor and metrics
  → "(planned)"; fault tolerance and the whole Failure semantics section → framed as
  intended design, not working behavior; retries invariant → removed from the list
  (no mechanism to guarantee); scale claim → "tens to thousands today, hundreds of
  thousands blocked on F10"; invariants-checked claim → narrowed to capacity +
  work-conservation only. Lease fencing was kept as a design description but reworded
  to the honest version: at-most-once currently holds only because the simulator never
  delivers a stale completion event (F1) — which reads as deeper understanding than the
  overclaim did. Cancellation narrowed to queued-work-only.
- **Decision / outcome:** README now honestly separates what exists from what's planned.
  Verified the two at-risk edits by reading the file (not the diff): the Failure
  semantics section is coherent, and the architecture diagram still renders (the
  "(planned: Docker...)" text wraps inside the box rather than overflowing). The audit
  also surfaced that F1 (lease fencing) is a stronger next-build candidate than the
  warm-cache discount — it converts a just-softened claim back to true and closes a real
  correctness gap.
- **Artifacts:** README.md corrections; this entry. No code changed. Audit was
  report-then-wait; corrections applied finding-by-finding, not bulk.
