# ADR-0001: Reject unplaceable jobs at admission, not inside the policy

- Status: accepted
- Date: 2026-07-24

## Context

FIFO is strict head-of-line: it dispatches the oldest pending job or Holds
the whole queue behind it (see `docs/design/scheduler-seam-spec.md`, minimal
vertical slice). A job whose `Needs` exceed every worker's capacity can
never be placed by any policy. Under strict FIFO, such a job sits at the
head of `Pending()` forever — no event ever removes it — so it Holds
indefinitely and every job behind it starves. This violates the
non-negotiable "every submitted job is eventually scheduled" invariant
(`CLAUDE.md`).

The vertical slice's original tests used uniform job/worker sizing and
never exercised a job that couldn't fit anywhere, so the bug shipped
undetected.

## Decision

A job whose `Needs` exceed the maximum capacity of every registered worker
is rejected at submission — it never enters the schedulable pending set.
This check lives in `Scheduler.apply`'s handling of `JobSubmitted`
(`scheduler/scheduler.go`), not inside the `Policy`: admissibility is a
property of the job against the worker pool, independent of which policy
is scheduling, so it belongs at the submission/orchestration layer the
scheduler core owns, not duplicated into every policy. The rejection is
still recorded as a first-class `Decision` (`Outcome: Reject`) so it's
observable in the decision log like any other scheduling action.

FIFO itself keeps strict head-of-line behavior — no backfill. With
admission now guaranteeing that any job which *does* enter the pending
queue can eventually fit some worker once capacity frees, strict FIFO no
longer causes permanent starvation; it only ever Holds a job that is
genuinely, temporarily blocked by current occupancy. Backfill (letting a
smaller job behind the head jump ahead while the head waits) remains a
deliberate non-goal for this slice, matching `CLAUDE.md` scope discipline —
it's a distinct policy behavior, not a correctness requirement.

## Consequences

- The no-starvation invariant holds for strict FIFO without adding
  backfill or any other change to `FIFO.Schedule`.
- Admission is checked against *currently registered* workers at
  submission time. If no worker is registered yet, the job is
  provisionally admitted (admissibility can't be determined). The gap this
  left — a job submitted before any worker exists could stay wedged in the
  pending set forever if the eventual worker pool could never fit it — is
  closed: `Scheduler.apply`'s `WorkerRegistered` handling re-runs admission
  over the pending set and rejects any now-unplaceable jobs
  (`rejectUnplaceablePending` in `scheduler/scheduler.go`).
- A rejected job produces no `Assignment` and no completion event; there is
  no retry or resubmission path in this slice. Anything that wants one
  (e.g. an orchestrator letting a human resize the job) is future work.
- `api.Outcome` gained a third value, `Reject`, alongside `Dispatch` and
  `Hold`. Only the admission check produces it — no `Policy` implementation
  should.

## Alternatives considered

- **Have `FIFO.Schedule` skip over unplaceable jobs instead of holding on
  them.** Rejected: that's backfill in disguise (a later job jumps the
  head), which we're explicitly not doing in this slice, and it would bury
  a cross-cutting admission concern inside one specific policy.
- **Cap `Needs` at submission instead of rejecting.** Rejected: silently
  changing what a job asked for hides a real scheduling failure and would
  produce jobs that lie about their resource requirements.
- **Leave strict FIFO unpatched and add backfill instead.** Rejected by the
  maintainer for this slice: backfill is a real policy behavior with its
  own fairness trade-offs and belongs in a deliberate future ADR, not as a
  side effect of fixing a starvation bug.
