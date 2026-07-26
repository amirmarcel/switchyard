# Known issues

Deferred findings from `docs/reviews/2026-07-25-opus-code-review.md`, each scheduled
against the milestone that makes it matter. Items graduate out of this file into the
build log when fixed. This file should shrink over time.

## Scheduled — before the chaos experiment

- **F1 (CRITICAL): lease fencing is structurally absent.** `Assignment.LeaseID` is
  written but never read; `api.JobCompleted` carries no lease token, so a stale
  completion can free a live assignment (breaks at-most-once + capacity). Requires an
  event-type change and a design call on lease flow. Prerequisite for failure injection.
- **F5 (MED-HIGH): at-most-once checker counts dispatches, so any legitimate retry reads
  as a violation.** Coupled to F1 — both stem from the decision log recording dispatches
  but not completions. Every benchmark with a retry will fail its invariant check the
  moment failure injection lands.

## Scheduled — before the real (Docker) executor

- **F7 (MED): `enactDispatch` panics on executor error, after consuming capacity.** Fine
  for the sim; a real executor's dispatch failures (image pull, daemon down) are normal
  operational conditions, not panics. Also fix the ordering so capacity isn't consumed
  before dispatch succeeds.
- **F8 (MED): `Cancel` is a no-op (`return nil`) in both executors.** In-flight work keeps
  running and still fires completion; the scheduler is protected only by the unfenced
  worker-ID match from F1. Cancellation doesn't actually stop work.
- **F6 (MED): `JobFailed` retries are unbounded and unattributed.** No attempt counter,
  cap, backoff, or fail-vs-infra distinction; a deterministically-failing job live-locks.
  Latent in sim (no failure source); on the critical path for chaos.

## Scheduled — before the scaling / dynamic-pool work

- **F9 (MED): re-registering a worker ID is silently dropped**, so the pool can never
  change. Admission is a one-shot snapshot never revisited against a changing pool
  (related to F2, which Option A fixes for the pending-set case).
- **F10 (MED): `snapshot()` is O(all jobs ever submitted) per event** (completed jobs are
  never pruned) → O(N²) per run. Caps the "scaling backend" well below its stated target.
  Fix with an ordered pending structure.

## Scheduled — dependency semantics (revisit with DAG/workflow work)

- **F4 (HIGH): a dependency that can never complete silently starves its dependents with
  no decision logged.** Reachable via rejected-dep, never-submitted-dep, or
  cancelled-while-running-dep. The dependent vanishes from the decision log entirely.
  Caught inside a benchmark (as starvation) but the scheduler has no diagnostic.

- **F15 (INFO): checkWorkConservation ignores Job.Deps in its pending-fit check.**
  Counts a dependency-blocked job as a valid "would fit" candidate, so a legitimate
  hold could be flagged as an unexplained work-conservation violation once a Deps-using
  workload exists. Latent — no current scenario uses Deps. Fix alongside dependency-aware
  scheduling (with F4).

## LOW — batch-fix when convenient

- **F11–F14:** BurstParams input validation (negative window/duration panics);
  warm-up exclusion specified but unimplemented; `WorkerUtilization` is CPU-only but
  generically named; `DecisionLog()` shallow-copies (shared `Factors` maps). Group-fix.

- **F16 (LOW/perf):** checkWorkConservation's pending-fit loop is O(holds × jobs × workers).
  The capacity-reconstruction quadratic was fixed (single sweep); the remaining cost is checking, per hold, whether any pending job would fit — ~49M lookups on the 200-job affinity scenario, ~11s/run under -race. Mitigated by a testing.Short() skip on the fast path. Optimize (e.g. track a running "smallest pending job that fits" rather than re-scanning) if/when it bites the full suite meaningfully.

## Explicitly not tracked

Pure style nits (§6 of the review) are left to be swept up as files are touched.
