⏺ I read CLAUDE.md, README.md, both design specs, both ADRs, the build log, and all 2,070 lines of Go. I then built a throwaway module outside the repo (replace directive, no repo files touched) to
  execute the paths I suspected. Every "confirmed" below is an actual run, not reasoning.

  Repo state: go vet clean, go test -race ./... green.

  ---
  Findings

  1. Invariant safety

  F1 — CRITICAL: an unfenced completion frees a live assignment, breaking at-most-once and capacity

  scheduler/scheduler.go:120-131

  JobCompleted is matched only on (job ID, worker ID). Assignment.LeaseID is generated at scheduler.go:211 and never read by anything — api.JobCompleted carries no lease token, so fencing is
  structurally impossible today. grep confirms LeaseID has exactly one occurrence in the repo, the write.

  Reproduction (run, output verbatim):

  t=0   DISPATCH j  w0     job j run#1 starts
  t=10  JobFailed j w0  →  DISPATCH j  w0     run#1 released, retried as run#2
  t=20  JobCompleted{j,w0} — run#1's *original* completion lands late
  t=30  DISPATCH k  w0     ← k dispatched to w0 while j's run#2 is still live

  w0 has capacity 1000; j and k each need 1000. The stale completion matched run#2's assignment, released its capacity, and marked j completed. enactDispatch's capacity assertion (scheduler.go:203)
  did not fire because the scheduler's own bookkeeping had already been corrupted — the assertion cannot catch this class.

  This is precisely the "briefly-partitioned worker double-completes work that was already rescheduled" scenario README.md:161 says lease fencing prevents. It needs no resubmission API — only
  JobFailed + a delayed completion, which is the normal Docker/chaos path. A second reachable variant is cancel→resubmit of the same JobID (also confirmed).

  Caught by a test? No. No test in the repo emits JobFailed or CancelRequested at all.

  The comment at scheduler.go:123-126 calling worker-ID matching "the at-most-once safeguard" overclaims — it's a proxy that holds only while a JobID can enter running at most once in a process's
  lifetime.

  ---
  F2 — CRITICAL: ADR-0001's starvation bug is fully reachable by reordering worker registration

  scheduler/scheduler.go:164-167 (provisional admission) + scheduler.go:112-118 (no re-check on WorkerRegistered)

  admissible returns true unconditionally when no worker is registered yet, and admission is never revisited when a worker later joins. Reproduction:

  push JobSubmitted{huge, needs 9000} at t=0
  push WorkerRegistered{w0, cap 1000} at t=1
  push JobSubmitted{normal-0,1,2, needs 100} at t=2,3,4

  decision log:
    t=0  HOLD huge
    t=1  HOLD huge
    t=2  HOLD huge
    t=3  HOLD huge
    t=4  HOLD huge

  normal-0/1/2 are never dispatched and never rejected — permanently starved. This is the identical failure ADR-0001 was written to eliminate, resurrected purely by event ordering.

  I'm not re-litigating the ADR: it names this gap in its Consequences. But it frames the consequence as being about the one unplaceable job ("this is not exercised, but it's a known gap"), when
  the actual blast radius is the entire queue behind it, i.e. the full original bug. And the real driver's defining property is that workers join dynamically off a live channel
  (executor/driver.go:11), so "drivers always register workers first" is a property of today's tests, not of the backend. The fix doesn't touch FIFO or require backfill: re-run admission over the
  pending set when WorkerRegistered fires (or reject-on-no-workers instead of admit).

  Caught by a test? No. bench.Run (bench/runner.go:50-55) always pushes every WorkerRegistered before any JobSubmitted, so the harness structurally cannot reach it.

  ---
  F3 — HIGH: conditional work-conservation is violated on the shipped scenario, and nothing checks it

  scheduler/fifo.go:26-35, bench/invariants.go:21-51

  CLAUDE.md states a policy may Hold while a runnable job and free capacity exist only if it explicitly reserves capacity, and that "an unexplained hold is a bug." FIFO reserves nothing — it
  head-of-line blocks — and its Hold is labeled Factors: {"no_capacity": 1}, which is factually wrong whenever capacity exists but is merely too small for the head job.

  I instrumented the shipped burst scenario with a wrapper policy delegating to scheduler.FIFO:

  total Hold decisions: 354
  Holds where free capacity existed that a queued job would fit: 164
  first: t=5014 — held, but pending job burst-job-0138 (cpu=415)
          fits worker burst-worker-000 (free cpu=535)
  InvariantsHeld = true   ← the harness reports the run as clean

  So 46% of the canonical scenario's holds are non-work-conserving by the letter of the invariant, on heterogeneous job sizes (250–1000 CPU against 2000-CPU workers) — this is the default behavior,
  not an edge case.

  ADR-0001 deliberately keeps strict FIFO and defers backfill, and I'm not arguing with that. But the ADR reconciles strict FIFO only with the starvation invariant; it never addresses the
  work-conservation invariant, which strict FIFO breaches as literally written. Two things are needed and neither exists: (a) an explicit "FIFO declares non-work-conservation" exemption recorded
  somewhere, and (b) a checker. bench.CheckInvariants verifies starvation, at-most-once, and capacity — work-conservation is absent entirely, so the one invariant that is currently being violated
  is the one the harness can't see. That's the exact shape of the two bugs that already shipped.

  The no_capacity factor should also distinguish "no free capacity anywhere" from "head job doesn't fit, capacity reserved for it" — otherwise the decision log can't be audited for this at all,
  which defeats the purpose of Factors.

  Caught by a test? No. Zero tests assert on api.Hold (grep: 0 occurrences in *_test.go).

  ---
  F4 — HIGH: a dependency that can never complete silently starves its dependents, with no decision logged

  scheduler/scheduler.go:278-285 (depsSatisfied), interacting with the Reject path (:92-106) and CancelRequested (:140-150)

  depsSatisfied requires s.completed[dep]. Three reachable ways a dep never reaches completed:
  F2 — CRITICAL: ADR-0001's starvation bug is fully reachable by reordering worker registration

  scheduler/scheduler.go:164-167 (provisional admission) + scheduler.go:112-118 (no re-check on WorkerRegistered)

  admissible returns true unconditionally when no worker is registered yet, and admission is never revisited when a worker later joins. Reproduction:

  push JobSubmitted{huge, needs 9000} at t=0
  push WorkerRegistered{w0, cap 1000} at t=1
  push JobSubmitted{normal-0,1,2, needs 100} at t=2,3,4

  decision log:
    t=0  HOLD huge
    t=1  HOLD huge
    t=2  HOLD huge
    t=3  HOLD huge
    t=4  HOLD huge

  normal-0/1/2 are never dispatched and never rejected — permanently starved. This is the identical failure ADR-0001 was written to eliminate, resurrected purely by event ordering.

  I'm not re-litigating the ADR: it names this gap in its Consequences. But it frames the consequence as being about the one unplaceable job ("this is not exercised, but it's a known gap"), when
  the actual blast radius is the entire queue behind it, i.e. the full original bug. And the real driver's defining property is that workers join dynamically off a live channel
  (executor/driver.go:11), so "drivers always register workers first" is a property of today's tests, not of the backend. The fix doesn't touch FIFO or require backfill: re-run admission over the
  pending set when WorkerRegistered fires (or reject-on-no-workers instead of admit).

  Caught by a test? No. bench.Run (bench/runner.go:50-55) always pushes every WorkerRegistered before any JobSubmitted, so the harness structurally cannot reach it.

  ---
  F3 — HIGH: conditional work-conservation is violated on the shipped scenario, and nothing checks it

  scheduler/fifo.go:26-35, bench/invariants.go:21-51

  CLAUDE.md states a policy may Hold while a runnable job and free capacity exist only if it explicitly reserves capacity, and that "an unexplained hold is a bug." FIFO reserves nothing — it
  head-of-line blocks — and its Hold is labeled Factors: {"no_capacity": 1}, which is factually wrong whenever capacity exists but is merely too small for the head job.

  The structural cause: bench reconstructs everything from the decision log, and the decision log records dispatches but not completions. The scheduler exposes no completion record.Worth deciding
  now whether Decision gets an attempt/lease field or the scheduler exposes a completion log — either way the checker needs to distinguish "dispatched twice" from "completed twice."

  Caught by a test? No — and inversely, no test asserts that a legitimate retry passes the checker.

  ---
  F6 — MEDIUM: JobFailed retries are unbounded and unattributed

  scheduler/scheduler.go:132-138

  JobFailed releases the assignment and marks nothing, so the job re-enters Pending() and is re-dispatched on the next policy pass. There is no attempt counter, no cap, no backoff, and no
  distinction between a job that failed (nonzero exit — should not be retried indefinitely) and infra failure (should be). A deterministically-failing job live-locks: dispatch → fail → dispatch,
  forever, consuming a worker slot each cycle. In the sim this is unreachable (no failure source), so it's latent — but it's on the critical path for the chaos experiment.

  Caught by a test? No.

  ---
  F7 — MEDIUM: enactDispatch panics on executor error, after consuming capacity

  scheduler/scheduler.go:219-221

  The capacity/unknown-job/already-running panics at :190-205 are defensible assertions — they catch policy bugs loudly, which fits the design. :219 is different: executor.Dispatch failing is a
  normal operational condition for the future Docker executor (image pull failure, daemon unreachable, OOM). Panicking takes the whole scheduler down, and RunReal (executor/driver.go:11) offers no
  recovery.

  Note also the ordering: the assignment is recorded and capacity consumed at :214-217 before Dispatch is called. If this is ever softened to return an error instead o
  deterministically-failing job live-locks: dispatch → fail → dispatch, forever, consuming a worker slot each cycle. In the sim this is unreachable (no failure
  source), so it's latent — but it's on the critical path for the chaos experiment.

  Caught by a test? No.

  ---
  F7 — MEDIUM: enactDispatch panics on executor error, after consuming capacity

  scheduler/scheduler.go:219-221

  The capacity/unknown-job/already-running panics at :190-205 are defensible assertions — they catch policy bugs loudly, which fits the design. :219 is different:
  executor.Dispatch failing is a normal operational condition for the future Docker executor (image pull failure, daemon unreachable, OOM). Panicking takes the whole
  scheduler down, and RunReal (executor/driver.go:11) offers no recovery.

  Note also the ordering: the assignment is recorded and capacity consumed at :214-217 before Dispatch is called. If this is ever softened to return an error instead
  of panicking, capacity leaks permanently unless the assignment is rolled back. Worth fixing the ordering now even while keeping the panic.

  ---
  F8 — MEDIUM: cancellation does not actually propagate to running work

  executor/fake.go:31, simulation/executor.go:27, scheduler/scheduler.go:145

  Both Cancel implementations are return nil — they claim success and do nothing. FakeExecutor's in-flight goroutine keeps running and still fires JobCompleted; the
  sim's already-queued completion event is never withdrawn. The scheduler discards the result (_ = s.executor.Cancel(...)), so even a real error would be invisible.

  Today the scheduler is protected because it drops the orphaned completion — but that protection is exactly the unfenced worker-ID match from F1. The invariant
  README.md:144 claims is currently held only by the scheduler ignoring events, not by anything actually stopping work.


  Both Cancel implementations are return nil — they claim success and do nothing. FakeExecutor's in-flight goroutine keeps running and still fires JobCompleted;
  the sim's already-queued completion event is never withdrawn. The scheduler discards the result (_ = s.executor.Cancel(...)), so even a real error would be
  invisible.

  Today the scheduler is protected because it drops the orphaned completion — but that protection is exactly the unfenced worker-ID match from F1. The invariant
  README.md:144 claims is currently held only by the scheduler ignoring events, not by anything actually stopping work.

  Caught by a test? No. CancelRequested appears in zero test files — the entire cancellation path is untested.

  ---
  F9 — MEDIUM: WorkerRegistered for an existing ID is silently dropped, so the pool can never change

  scheduler/scheduler.go:112-118

  Both Cancel implementations are return nil — they claim success and do nothing. FakeExecutor's in-flight goroutine keeps running and still fires
  JobCompleted; the sim's already-queued completion event is never withdrawn. The scheduler discards the result (_ = s.executor.Cancel(...)), so even a real
  error would be invisible.

  Today the scheduler is protected because it drops the orphaned completion — but that protection is exactly the unfenced worker-ID match from F1. The
  invariant README.md:144 claims is currently held only by the scheduler ignoring events, not by anything actually stopping work.

  Caught by a test? No. CancelRequested appears in zero test files — the entire cancellation path is untested.
  admissible returns true unconditionally when no worker is registered yet, and admission is never revisited when a worker later joins. Reproduction:

  push JobSubmitted{huge, needs 9000} at t=0
  push WorkerRegistered{w0, cap 1000} at t=1
  push JobSubmitted{normal-0,1,2, needs 100} at t=2,3,4

  decision log:
    t=0  HOLD huge
    t=1  HOLD huge
    t=2  HOLD huge
    t=3  HOLD huge
    t=4  HOLD huge

  normal-0/1/2 are never dispatched and never rejected — permanently starved. This is the identical failure ADR-0001 was written to eliminate, resurrected
  purely by event ordering.

  I'm not re-litigating the ADR: it names this gap in its Consequences. But it frames the consequence as being about the one unplaceable job ("this is not
  exercised, but it's a known gap"), when the actual blast radius is the entire queue behind it, i.e. the full original bug. And the real driver's defining
  property is that workers join dynamically off a live channel (executor/driver.go:11), so "drivers always register workers first" is a property of today's
  tests, not of the backend. The fix doesn't touch FIFO or require backfill: re-run admission over the pending set when WorkerRegistered fires (or
  reject-on-no-workers instead of admit).

  Caught by a test? No. bench.Run (bench/runner.go:50-55) always pushes every WorkerRegistered before any JobSubmitted, so the harness structurally cannot
  reach it.

  ---
  F3 — HIGH: conditional work-conservation is violated on the shipped scenario, and nothing checks it

  scheduler/fifo.go:26-35, bench/invariants.go:21-51

  CLAUDE.md states a policy may Hold while a runnable job and free capacity exist only if it explicitly reserves capacity, and that "an unexplained hold is a
  bug." FIFO reserves nothing — it head-of-line blocks — and its Hold is labeled Factors: {"no_capacity": 1}, which is factually wrong whenever capacity
  exists but is merely too small for the head job.

  One weakness worth naming, though it isn't a bug: RunHarness's determinism guard (harness.go:64-69) re-runs the same in-process deterministic code five
  times. It would catch a regression that introduced raw map iteration (Go randomizes that per-range), which is the main thing it's for — so it has real
  value. But it cannot catch cross-process or cross-version divergence, and reflect.DeepEqual is not the "byte-identical decision log" that CLAUDE.md and
  ADR-0002 promise. Serializing the log and comparing bytes would make the claim literally true and would additionally pin Factors map content.

  ---
  3. The backend seam — clean

  The core is genuinely backend-agnostic. Import graph:

  - api/ → nothing
  - scheduler/ → fmt, sort, api — no time, no channels, no goroutines, no sim or Docker concepts
  - simulation/ → api, scheduler
  - executor/ → api, scheduler, time
  - bench/ → api, scheduler, simulation (spec-sanctioned, inward-only)

  All concurrency lives in executor/fake.go:24 (one goroutine per dispatch) and the channel in driver.go. The scheduler reads time only via s.clock.Now() at
  scheduler.go:52. No leak found.

  Two seam observations, neither a defect today:

  The "same scheduler code, both backends" claim is asserted but not compared. The seam spec's acceptance bar (scheduler-seam-spec.md:246) is explicit: "The
  two need not match on timing, but must match on which job went where under FIFO." scheduler_test.go and executor/driver_test.go each independently assert
  dispatched-exactly-once on the same 20-job/3-worker/1000-CPU shape, but no test compares the two decision logs' job→worker mapping. The one acceptance
  criterion that actually proves the dual-backend claim is the one not asserted. This is cheap to add and is the single highest-value test in the repo given
  it's the load-bearing thesis.
  admissible returns true unconditionally when no worker is registered yet, and admission is never revisited when a worker later joins. Reproduction:

  push JobSubmitted{huge, needs 9000} at t=0
  push WorkerRegistered{w0, cap 1000} at t=1
  push JobSubmitted{normal-0,1,2, needs 100} at t=2,3,4

  decision log:
    t=0  HOLD huge
    t=1  HOLD huge
    t=2  HOLD huge
    t=3  HOLD huge
    t=4  HOLD huge

  normal-0/1/2 are never dispatched and never rejected — permanently starved. This is the identical failure ADR-0001 was written to eliminate, resurrected
  purely by event ordering.

  I'm not re-litigating the ADR: it names this gap in its Consequences. But it frames the consequence as being about the one unplaceable job ("this is not
  exercised, but it's a known gap"), when the actual blast radius is the entire queue behind it, i.e. the full original bug. And the real driver's defining
  property is that workers join dynamically off a live channel (executor/driver.go:11), so "drivers always register workers first" is a property of today's
  tests, not of the backend. The fix doesn't touch FIFO or require backfill: re-run admission over the pending set when WorkerRegistered fires (or

  normal-0/1/2 are never dispatched and never rejected — permanently starved. This is the identical failure ADR-0001 was written to eliminate, resurrected purely by event ordering.

  The comment at scheduler.go:123-126 calling worker-ID matching "the at-most-once safeguard" overclaims — it's a proxy that holds only while a JobID can enter running at most once
  in a process's lifetime.

  ---
  F2 — CRITICAL: ADR-0001's starvation bug is fully reachable by reordering worker registration

  scheduler/scheduler.go:164-167 (provisional admission) + scheduler.go:112-118 (no re-check on WorkerRegistered)

  admissible returns true unconditionally when no worker is registered yet, and admission is never revisited when a worker later joins. Reproduction:

  push JobSubmitted{huge, needs 9000} at t=0
  push WorkerRegistered{w0, cap 1000} at t=1
  push JobSubmitted{normal-0,1,2, needs 100} at t=2,3,4

  decision log:
    t=0  HOLD huge
    t=1  HOLD huge
    t=2  HOLD huge
    t=3  HOLD huge
    t=4  HOLD huge

  normal-0/1/2 are never dispatched and never rejected — permanently starved. This is the identical failure ADR-0001 was written to eliminate, resurrected purely by event ordering.

  I'm not re-litigating the ADR: it names this gap in its Consequences. But it frames the consequence as being about the one unplaceable job ("this is not exercised, but it's a known
  gap"), when the actual blast radius is the entire queue behind it, i.e. the full original bug. And the real driver's defining property is that workers join dynamically off a live
  channel (executor/driver.go:11), so "drivers always register workers first" is a property of today's tests, not of the backend. The fix doesn't touch FIFO or require backfill:
  re-run admission over the pending set when WorkerRegistered fires (or reject-on-no-workers instead of admit).

  Caught by a test? No. bench.Run (bench/runner.go:50-55) always pushes every WorkerRegistered before any JobSubmitted, so the harness structurally cannot reach it.

  ---
  F3 — HIGH: conditional work-conservation is violated on the shipped scenario, and nothing checks it

  scheduler/fifo.go:26-35, bench/invariants.go:21-51

  CLAUDE.md states a policy may Hold while a runnable job and free capacity exist only if it explicitly reserves capacity, and that "an unexplained hold is a bug." FIFO reserves
  nothing — it head-of-line blocks — and its Hold is labeled Factors: {"no_capacity": 1}, which is factually wrong whenever capacity exists but is merely too small for the head job.

  I instrumented the shipped burst scenario with a wrapper policy delegating to scheduler.FIFO:

  total Hold decisions: 354
  Holds where free capacity existed that a queued job would fit: 164
  first: t=5014 — held, but pending job burst-job-0138 (cpu=415)
          fits worker burst-worker-000 (free cpu=535)
  InvariantsHeld = true   ← the harness reports the run as clean

  So 46% of the canonical scenario's holds are non-work-conserving by the letter of the invariant, on heterogeneous job sizes (250–1000 CPU against 2000-CPU workers) — this is the
  default behavior, not an edge case.

  ADR-0001 deliberately keeps strict FIFO and defers backfill, and I'm not arguing with that. But the ADR reconciles strict FIFO only with the starvation invariant; it never
  addresses the work-conservation invariant, which strict FIFO breaches as literally written. Two things are needed and neither exists: (a) an explicit "FIFO declares
  non-work-conservation" exemption recorded somewhere, and (b) a checker. bench.CheckInvariants verifies starvation, at-most-once, and capacity — work-conservation is absent
  entirely, so the one invariant that is currently being violated is the one the harness can't see. That's the exact shape of the two bugs that already shipped.

  The no_capacity factor should also distinguish "no free capacity anywhere" from "head job doesn't fit, capacity reserved for it" — otherwise the decision log can't be audited for
  this at all, which defeats the purpose of Factors.

  Caught by a test? No. Zero tests assert on api.Hold (grep: 0 occurrences in *_test.go).

  ---
  F4 — HIGH: a dependency that can never complete silently starves its dependents, with no decision logged

  scheduler/scheduler.go:278-285 (depsSatisfied), interacting with the Reject path (:92-106) and CancelRequested (:140-150)

  depsSatisfied requires s.completed[dep]. Three reachable ways a dep never reaches completed:

  ┌─────────────────────────────────────────────────────────────────────────────────────┬──────────────────────────────────────────────────────────────────────────┐
  │                                        Case                                         │                          Confirmed decision log                          │
  ├─────────────────────────────────────────────────────────────────────────────────────┼──────────────────────────────────────────────────────────────────────────┤
  │ Dep rejected at admission                                                           │ t=0 REJECT huge / t=0 DISPATCH other — child produces no decision at all │
  ├─────────────────────────────────────────────────────────────────────────────────────┼──────────────────────────────────────────────────────────────────────────┤
  │ Dep never submitted (typo'd/pruned DAG)                                             │ t=0 DISPATCH other — child absent                                        │
  ├─────────────────────────────────────────────────────────────────────────────────────┼──────────────────────────────────────────────────────────────────────────┤
  │ Dep cancelled while running (:148 deletes it from s.jobs without marking completed) │ t=0 DISPATCH parent — child absent                                       │
  └─────────────────────────────────────────────────────────────────────────────────────┴──────────────────────────────────────────────────────────────────────────┘

  The dependent is filtered out of Pending() (scheduler.go:235), so it isn't even eligible to produce a Hold. It vanishes from the decision log entirely — no Reject, no Hold,
  nothing. Given the log is billed as the unit of observability and replay (README.md:128), a job that leaves zero trace is the worst possible failure mode.

  bench.CheckInvariants does catch it as job child never dispatched or rejected (starvation) (I confirmed this) — credit where due, the log-reconstruction check is genuinely
  load-bearing here. But that only helps inside a benchmark whose Workload lists the job; the scheduler itself has no diagnostic and no test.

  Related: cancellation propagating to a job's dependents is unspecified. README.md:144 says cancellation propagates to "running and queued work"; a queued dependent of a cancelled
  job is queued work that can now never run.

  Caught by a test? No. Deps appears in zero test files.

  ---
  F5 — MEDIUM-HIGH: the at-most-once checker counts dispatches, so any legitimate retry reads as a violation

  bench/invariants.go:41-42, bench/runner.go:81-84

  case dispatches > 1: "at-most-once violated". But at-most-once means at most one execution/completion, and CLAUDE.md separately requires "retries preserve idempotency" — retries
  are mandatory behavior, and every retry is a second Dispatch decision. Confirmed:

  4 × JobFailed → log: 5 DISPATCH j decisions
  bench.CheckInvariants: [job j dispatched 5 times (at-most-once violated)]

  The moment failure injection or the chaos experiment lands (README.md:233), every benchmark run with a single retry will fail its invariant check. Result.Completed = dispatched
  (runner.go:82, 116) has the same conflation — it counts dispatches, not completions, and would over-report.

  The structural cause: bench reconstructs everything from the decision log, and the decision log records dispatches but not completions. The scheduler exposes no completion record.
  Worth deciding now whether Decision gets an attempt/lease field or the scheduler exposes a completion log — either way the checker needs to distinguish "dispatched twice" from
  "completed twice."

  Caught by a test? No — and inversely, no test asserts that a legitimate retry passes the checker.

  ---
  F6 — MEDIUM: JobFailed retries are unbounded and unattributed

  scheduler/scheduler.go:132-138

  JobFailed releases the assignment and marks nothing, so the job re-enters Pending() and is re-dispatched on the next policy pass. There is no attempt counter, no cap, no backoff,
  and no distinction between a job that failed (nonzero exit — should not be retried indefinitely) and infra failure (should be). A deterministically-failing job live-locks: dispatch
  → fail → dispatch, forever, consuming a worker slot each cycle. In the sim this is unreachable (no failure source), so it's latent — but it's on the critical path for the chaos
  experiment.

  Caught by a test? No.

  ---
  F7 — MEDIUM: enactDispatch panics on executor error, after consuming capacity

  scheduler/scheduler.go:219-221

  The capacity/unknown-job/already-running panics at :190-205 are defensible assertions — they catch policy bugs loudly, which fits the design. :219 is different: executor.Dispatch
  failing is a normal operational condition for the future Docker executor (image pull failure, daemon unreachable, OOM). Panicking takes the whole scheduler down, and RunReal
  (executor/driver.go:11) offers no recovery.

  Note also the ordering: the assignment is recorded and capacity consumed at :214-217 before Dispatch is called. If this is ever softened to return an error instead of panicking,
  capacity leaks permanently unless the assignment is rolled back. Worth fixing the ordering now even while keeping the panic.

  ---
  F8 — MEDIUM: cancellation does not actually propagate to running work

  executor/fake.go:31, simulation/executor.go:27, scheduler/scheduler.go:145

  Both Cancel implementations are return nil — they claim success and do nothing. FakeExecutor's in-flight goroutine keeps running and still fires JobCompleted; the sim's
  already-queued completion event is never withdrawn. The scheduler discards the result (_ = s.executor.Cancel(...)), so even a real error would be invisible.

  Today the scheduler is protected because it drops the orphaned completion — but that protection is exactly the unfenced worker-ID match from F1. The invariant README.md:144 claims
  is currently held only by the scheduler ignoring events, not by anything actually stopping work.

  Caught by a test? No. CancelRequested appears in zero test files — the entire cancellation path is untested.

  ---
  F9 — MEDIUM: WorkerRegistered for an existing ID is silently dropped, so the pool can never change

  scheduler/scheduler.go:112-118

  Re-registering a worker with different capacity is a no-op returning changed=false. Confirmed:

  register w0 cap 1000
  submit big  (4000) → REJECT
  register w0 cap 8000   ← silently ignored
  submit big2 (4000) → REJECT   ← still rejected against the stale 1000

  Combined with F2, the theme is: admission is a one-shot snapshot that is never revisited against a changing worker pool. For a project whose roadmap includes autoscaling and whose
  real backend has dynamically-joining workers, this is a design decision worth making explicitly rather than by omission.

  ---
  2. Purity and determinism — this is solid

  I looked specifically for the failure modes you named and did not find them. Stating this plainly rather than manufacturing concerns:

  - FIFO.Schedule is genuinely pure. Reads only s api.State and now api.Time. No time.Now, no I/O, no globals, no randomness. fifo.go:17-20 builds a local free map but only ever
  indexes it by key from the already-ordered workers slice (firstFit, fifo.go:56) — it never ranges over it. This is the right pattern and it's applied correctly.
  - Every map range in scheduling logic is key-gathering followed by a sort. snapshot() (scheduler.go:227-273) ranges s.jobs/s.workers/s.running only to collect IDs, then sorts:
  pending by submissionSeq (unique per job → total order, not just a partial one), workers and running by ID (unique → total). No unstable tie-break anywhere. checkCapacityInvariant
  sorts workerIDs before iterating (invariants.go:88-92).
  - admissible (scheduler.go:168) ranges a map without sorting, and this is correct — it's a pure existence predicate whose boolean result is order-independent. The comment at
  :157-163 says exactly this. Right call, correctly justified.
  - The sim event queue's tie-break is total. eventHeap.Less (eventqueue.go:43-48) falls through to a monotonic seq; seq is unique, so ordering is a total order with no residual
  ambiguity. Matches the spec requirement.
  - Randomness is correctly scoped. GenerateBurst uses rand.New(rand.NewSource(p.Seed)) (workload.go:65), never the global. math/rand's NewSource stream is frozen by Go's
  compatibility promise, so seeds are stable across Go versions. sort.SliceStable at workload.go:102 preserves generation order for equal SubmitAt — deliberate and documented.
  - The sim clock only moves forward. Events pop in nondecreasing At order and the executor always schedules now + duration, so Clock.set never rewinds (given non-negative durations
  — see F13).

  One weakness worth naming, though it isn't a bug: RunHarness's determinism guard (harness.go:64-69) re-runs the same in-process deterministic code five times. It would catch a
  regression that introduced raw map iteration (Go randomizes that per-range), which is the main thing it's for — so it has real value. But it cannot catch cross-process or
  cross-version divergence, and reflect.DeepEqual is not the "byte-identical decision log" that CLAUDE.md and ADR-0002 promise. Serializing the log and comparing bytes would make the
  claim literally true and would additionally pin Factors map content.

  ---
  3. The backend seam — clean

  The core is genuinely backend-agnostic. Import graph:

  - api/ → nothing
  - scheduler/ → fmt, sort, api — no time, no channels, no goroutines, no sim or Docker concepts
  - simulation/ → api, scheduler
  - executor/ → api, scheduler, time
  - bench/ → api, scheduler, simulation (spec-sanctioned, inward-only)

  All concurrency lives in executor/fake.go:24 (one goroutine per dispatch) and the channel in driver.go. The scheduler reads time only via s.clock.Now() at scheduler.go:52. No leak
  found.

  Two seam observations, neither a defect today:

  The "same scheduler code, both backends" claim is asserted but not compared. The seam spec's acceptance bar (scheduler-seam-spec.md:246) is explicit: "The two need not match on
  timing, but must match on which job went where under FIFO." scheduler_test.go and executor/driver_test.go each independently assert dispatched-exactly-once on the same
  20-job/3-worker/1000-CPU shape, but no test compares the two decision logs' job→worker mapping. The one acceptance criterion that actually proves the dual-backend claim is the one
  not asserted. This is cheap to add and is the single highest-value test in the repo given it's the load-bearing thesis.

  The measurement layer has a sim-shaped assumption baked in. Workload.JobDuration is a single fixed value, and both computeResult (runner.go:83, 90) and checkCapacityInvariant
  (invariants.go:83-85) reconstruct occupancy windows as [d.At, d.At + w.JobDuration). That's correct for today's sim executor, which charges a constant duration. It becomes silently
  wrong the moment durations vary by job/class/cache-warmth (which modeledDuration in the seam spec anticipates) or the real-executor path lands — utilization and the capacity check
  would both be measuring a fiction, with no signal that they'd stopped being true. The fix direction is for the harness to derive intervals from actual completion records rather
  than a modeled constant; flagging now because it's a design coupling, not a bug to fix today.

  ---
  4. Test-coverage gaps that matter

  Ranked by how likely each is to hide a live invariant violation. You asked specifically for blind spots resembling the uniform-sized-data one — the top three are exactly that
  shape.

  G1 — scheduler_test.go retains the known-broken pairwise capacity checker, and passes only vacuously.
  scheduler/scheduler_test.go:245-252 still uses intervals[j].start < intervals[i].end && intervals[i].start < intervals[j].end — the over-counting pairwise check that
  bench/invariants.go:58-63 explicitly names as the bug it fixed, and that bench/invariants_test.go has three regression tests against. Two problems: it's a false-positive generator
  that will spuriously fail the moment a bin-packing scenario is added, and right now it proves nothing, because no scenario in the scheduler package ever places two jobs on one
  worker simultaneously:

  ┌──────────────────────────────┬────────────┬─────────────┬────────────────────────┐
  │           scenario           │ worker cap │  job needs  │ concurrent jobs/worker │
  ├──────────────────────────────┼────────────┼─────────────┼────────────────────────┤
  │ 20 jobs 3 workers (:50)      │ 1000       │ 1000        │ 1                      │
  ├──────────────────────────────┼────────────┼─────────────┼────────────────────────┤
  │ 5 jobs 2 workers (:51)       │ 1000       │ 1000        │ 1                      │
  ├──────────────────────────────┼────────────┼─────────────┼────────────────────────┤
  │ TestAdmissionRejects… (:143) │ 1000       │ 1000 / 5000 │ 1                      │
  ├──────────────────────────────┼────────────┼─────────────┼────────────────────────┤
  │ TestNoStarvation… (:185-188) │ 500, 1000  │ 1000        │ 1                      │
  └──────────────────────────────┴────────────┴─────────────┴────────────────────────┘

  So enactDispatch's partial-capacity arithmetic (scheduler.go:200-217) and FIFO's running-free decrement (fifo.go:46-49) are exercised only indirectly through bench. This is the
  same uniform-sizing blind spot as before, still present in the same file. Either delete the duplicate checker in favor of the fixed one, or fix it — but don't leave a checker in
  the tree that the codebase's own comments call wrong.

  G2 — Nothing tests Hold. Zero test files reference api.Hold. Given F3 (work-conservation is currently violated on the default scenario) this is the gap with a live bug behind it.

  G3 — Nothing tests Deps, CancelRequested, or JobFailed. Zero occurrences across all test files. These are three of the eight non-negotiable invariants (dependencies respected,
  cancellation propagates, retries preserve idempotency) — asserted by no test at any level. F1, F4, F5, F6, and F8 all live in this untested region, which is not a coincidence.

  G4 — No sim-vs-real decision-log comparison. See §3; it's the seam spec's own acceptance criterion.

  G5 — No test covers Workload job sizes bounded by MemBytes rather than CPUMillis. firstFit and the capacity checks are two-dimensional, but every scenario is CPU-bound. A bug in
  the MemBytes half of any of those conditionals would go undetected — WorkerUtilization (runner.go:96-107) is CPU-only by construction, so it wouldn't show there either.

  G6 — Nothing asserts the scheduler's own panic-assertions fire. scheduler.go:190, 194, 197, 204 are the last line of defense for capacity and at-most-once. A misbehaving policy
  (dispatching an unknown job, double-dispatching, over-committing) should be provably caught — that needs a deliberately-bad test policy, and there isn't one.

  ---
  5. Other correctness issues

  F10 — MEDIUM: snapshot() is O(all jobs ever submitted) per event. scheduler.go:227 ranges s.jobs, which is never pruned — completed jobs stay forever (only flagged in s.completed).
  With one policy pass per event and ~3 events per job, a run is O(N²): the 200-job burst does ~120k iterations, but the README's "hundreds of thousands of jobs" target
  (README.md:37) is ~10¹⁰. FIFO.Schedule compounds it — it re-scans the whole pending prefix each call. For the package whose stated job is scaling, this caps the scaling backend
  well below its design target. Not a correctness bug; flagging because it's cheap to fix now (maintain an ordered pending structure) and expensive later.

  F11 — LOW: no validation on BurstParams. workload.go:79 calls rng.Int63n(int64(p.ArrivalWindow)) — panics if ArrivalWindow < 0. JobCPUMax < JobCPUMin silently degrades to JobCPUMin
  (:82). A negative JobDuration would make the sim clock run backwards and produce negative queue delays. Cheap guard, and this is the harness's only public input surface.

  F12 — LOW: warm-up exclusion is specified but not implemented or documented. benchmark-harness-spec.md:146 requires "exclude the warm-up window from the reported percentiles
  (document how)" and README.md:194 promises "warm-up discarded — steady-state only." Nothing in bench/ implements or mentions it. The burst scenario has no ramp so it's arguably
  vacuous today, but the methodology checkbox is currently unmet, and RunAndReport output makes no such claim disclaimer.

  F13 — LOW: Result.WorkerUtilization is CPU-only but named generically. runner.go:96-107 divides busy CPU-time by total CPU capacity, ignoring MemBytes entirely. A mem-bound
  workload would report misleadingly low utilization. The field comment at runner.go:26 says "busy CPU-time / total available CPU-time" — correct, but the JSON field name and
  PrintSummary label (report.go:47, "worker utilization") don't carry the qualifier.

  F14 — LOW: DecisionLog() is a shallow copy. scheduler.go:76-80 copies the slice, but each Decision.Factors is a shared map. A caller mutating one corrupts the scheduler's log.
  Trivial today (nobody does), but it undercuts the "returns a copy" promise in the doc comment.

  ---
  6. Style / maintainability (short, as requested)

  - cap shadows the builtin at bench/invariants.go:96 and scheduler/scheduler_test.go:244. Harmless, but predeclared would flag it if enabled.
  - simulation/eventqueue.go:3-5 has two consecutive single-import statements; should be one grouped block. gofmt won't fix it, gci/goimports would.
  - bench/runner.go:83 computes makespan from dispatch times plus a modeled duration rather than from completions — correct today, but it's the same coupling as §3's second point and
  would be clearer named modeledMakespan.
  - FIFO's Dispatch decisions carry Factors: nil while Hold decisions carry factors (fifo.go:37-44 vs :26-33). The seam spec's Decision example shows factors on dispatches too;
  asymmetric population makes the log harder to diff between policies later.

  Doc comments are in good shape and match the CLAUDE.md rules — one package doc per package, file headers only where they earn their place, and I found no stale ones. The two
  exceptions are the overclaiming "this is the at-most-once safeguard" at scheduler.go:123-126 (see F1) and fifo.go:5-8, which describes head-of-line holding without noting it's
  non-work-conserving (see F3).

  ---
  7. On the deferred decisions

  Only two, briefly, and neither is "you should build X":

  ADR-0001's provisional-admission escape hatch should be closed now, not deferred. Not because the ADR is wrong — the reject-at-admission decision and the keep-strict-FIFO decision
  are both right — but because the escape hatch reopens the exact bug the ADR closes (F2), and closing it requires no policy change and no backfill. Re-running admission on
  WorkerRegistered is a handful of lines fully inside the scheduler core.

  Work-conservation needs a decision recorded before the second policy lands, not after. It's currently an invariant that CLAUDE.md calls non-negotiable, that FIFO violates 164 times
  in the shipped scenario, and that nothing measures. Once there's a candidate policy, "policy B holds capacity more often than FIFO" becomes a result you want to report — and
  you'll want the checker to have existed before the number did, otherwise it looks retrofitted to the answer. This is a one-paragraph ADR (FIFO declares non-work-conservation via
  head-of-line; hold reasons are enumerated; the harness counts them) plus a checker, not a feature.

  Everything else deferred — no Prometheus/OTel, no CLI, no second policy, sim-only benchmarks, no merge queue/autoscaler/remote cache, one scenario, no workload profiles — reads as
  correctly scoped, and the specs and ADRs justify each. I found no case where deferred scope was actually load-bearing for what's built.

  ---
  Highest-value first three: F1 (unfenced completion → capacity + at-most-once, reachable via the retry path), F2 (starvation reachable by event ordering), F3 (work-conservation
  violated on the default scenario, unchecked). All three sit in the untested Deps/Cancel/Fail/Hold region identified in G2–G3, which is the structural version of the same blind spot
  that produced the two bugs you already found.