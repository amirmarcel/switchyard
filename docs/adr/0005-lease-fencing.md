# ADR-0005: Lease fencing on completion/failure events

- Status: accepted
- Date: 2026-08-01

## Context

`docs/reviews/2026-07-25-opus-code-review.md` (finding F1, tracked as
`docs/known-issues.md` F1) found that at-most-once was not actually
enforced: `Assignment.LeaseID` was generated at dispatch but never read by
anything, and `api.JobCompleted`/`api.JobFailed` carried no lease token at
all, so fencing was structurally impossible. The scheduler matched a
completion against the running assignment on `(JobID, WorkerID)` only.

That match is exactly wrong for the case lease fencing exists to cover: a
job dispatched to worker `w0`, which then fails or is partitioned; the
scheduler releases the assignment and retries the job — often onto the same
worker `w0`, since nothing about a transient partition marks `w0` as
permanently unfit. The retried run gets a new assignment, but the *original*
run's completion can still arrive late (a briefly-partitioned worker
finishing work it no longer owns). Worker-ID matching cannot distinguish
"this completion belongs to the assignment I currently hold" from "this
completion belongs to a superseded assignment for the same job on the same
worker" — both have the same `(JobID, WorkerID)`. Only a token that changes
on every (re)dispatch can make that distinction. `README.md`'s Failure
semantics section already named this as the intended design
("worker dies → lease expires → job becomes runnable again → ... → a worker
claims the lease") but it was never wired into the completion path.

Coupled to this, `docs/known-issues.md` F5 found that
`bench.CheckInvariants`' at-most-once check counted `Dispatch` decisions,
so every legitimate retry — mandatory behavior under CLAUDE.md's "retries
preserve idempotency" — read as an at-most-once violation the moment
failure injection exists. The decision log had dispatches but no record of
completions at all, so the checker had no way to ask the question it
actually needed to ask: how many times did this job's execution get
accepted, not how many times was it attempted.

## Decision

**Every dispatch mints a fresh lease; every completion/failure is checked
against the assignment's current lease before being accepted.**

- `api.Decision` gains a `LeaseID` field. The scheduler (not the `Policy`)
  populates it in `enactDispatch`, at the same point it already generates
  `Assignment.LeaseID` — the policy has no lease to give; it only says what
  to dispatch and to where. The lease-carrying `Decision` is what's handed
  to `Executor.Dispatch`, so the executor can carry that same token through
  to whatever completion event it later emits.
- `api.JobCompleted` and `api.JobFailed` gain a `LeaseID` field. Both
  executors (`executor.FakeExecutor`, `simulation.Executor`) copy
  `Decision.LeaseID` into the `JobCompleted` they construct on dispatch —
  this is the propagation path, not a new capability of either executor.
- The scheduler's completion/failure handler now checks the assignment it
  holds for that job (`s.running[ev.Job]`) two ways:
  - No assignment exists at all → the event references a job that's
    already completed, cancelled, or never ran under this scheduler
    instance. Silently ignored — there's nothing to fence, and this is the
    ordinary shape of a duplicate/very-late event with no live state to
    corrupt.
  - An assignment exists, but its `Worker` or `LeaseID` doesn't match the
    event → **fenced**: rejected, not counted, current assignment left
    completely untouched. Logged as a `Fenced` `Decision` (a new
    `api.Outcome`) carrying the stale lease, so a fenced event is visible
    in the decision log exactly like any other scheduling action, per
    CLAUDE.md's "every scheduling action emits a first-class, logged
    Decision."
  - An assignment exists and both match → accepted. `JobCompleted` releases
    the assignment, marks the job completed, and now also logs a new
    `Completed` `Decision` — this is what lets `bench.CheckInvariants` (F5)
    count accepted completions instead of dispatches.
- Lease generation is deterministic: `fmt.Sprintf("%s-%d", jobID,
  leaseSeq)`, where `leaseSeq` is a monotonic counter on the `Scheduler`
  advanced once per dispatch, in event-processing order. No timestamp, no
  UUID, no unseeded randomness — the same event sequence always produces
  the same leases, which is what keeps the byte-identical decision-log
  invariant intact. This scheme already existed for `Assignment.LeaseID`;
  this change is what makes it load-bearing rather than write-only.

### Why a per-dispatch token beats worker-ID matching

Worker-ID matching fails exactly when a job is reassigned back to the
*same* worker after a transient partition — which is the common case, not
an edge case, since a briefly-partitioned worker typically remains in the
pool. The old completion's `(JobID, WorkerID)` is indistinguishable from
the new one's. A lease minted fresh on every dispatch is the only field
that actually changes across a reassignment, so it's the only field that
can answer "is this completion for the assignment I currently hold."

## Consequences

- At-most-once is now upheld by mechanism: a stale completion cannot free a
  live assignment or mark a job completed twice, regardless of *why* it's
  stale (retry-after-failure today; lease-expiry-after-partition once
  failure injection exists). This closes F1 without requiring the chaos
  harness to exist first — the fencing logic doesn't care what generated
  the stale event.
- `bench.CheckInvariants`' at-most-once check now counts `Completed`
  decisions per job, not `Dispatch` decisions (F5). A job legitimately
  dispatched twice (retried) but completed once is no longer flagged; two
  *accepted* completions for the same job still is.
- The decision log gains two new `Outcome`s (`Completed`, `Fenced`),
  visible to every consumer that walks `Scheduler.DecisionLog()`. Existing
  consumers (`bench/invariants.go`, `bench/runner.go`,
  `bench/metrics.go`, `scheduler_test.go`) all switch on specific `Outcome`
  values rather than exhaustively, so the new values are additive and
  don't change any existing case's behavior.
- This is a prerequisite for the failure-injection/chaos experiment
  (`docs/known-issues.md`, "Scheduled — before the chaos experiment"), not
  that experiment itself — no failure-injection harness, worker-kill
  simulation, or lease-expiry timer is added here. `JobFailed`'s unbounded
  retry (F6) and `Cancel`'s no-op propagation (F8) are unchanged; both
  remain separately tracked.

## Alternatives considered

- **Keep worker-ID matching, add a "generation" counter per worker instead
  of per dispatch.** Rejected: a worker-generation counter only fences
  events from a worker's *previous* lifetime (e.g. after it restarts), not
  a stale completion from the worker's *current* lifetime for a job it no
  longer holds — which is exactly the reassign-to-the-same-worker case that
  motivated this ADR.
- **Give the scheduler a completion log instead of adding lease fields to
  the events.** Considered in the original review (F5's "worth deciding
  now whether Decision gets an attempt/lease field or the scheduler exposes
  a completion log"). Rejected here: a side-channel completion log still
  needs *something* in the completion event to correlate against the
  correct attempt — the lease token is that correlator either way, so a
  separate log would be additional bookkeeping for the same information the
  event field already carries directly.
- **Timestamp- or UUID-based lease tokens.** Rejected outright: both break
  the mandatory determinism constraint (no wall-clock, no unseeded
  randomness in scheduling logic) and would make the byte-identical
  decision-log invariant unverifiable.
- **Fence only `JobCompleted`, leave `JobFailed` on worker-ID matching.**
  Rejected for consistency: a stale `JobFailed` under the old assignment
  could otherwise release the *current* assignment's capacity just as
  wrongly as a stale `JobCompleted` could mark it falsely done. Both event
  types carry the same failure shape, so both get the same fencing check.
