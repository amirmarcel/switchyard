# Priority + cache-affinity — implementation notes

Short design note for the non-obvious modeling calls made while implementing
`scheduler.PriorityAffinity` per ADR-0003. Not an ADR — no design-weight
decision here overrides ADR-0003; these are the specific calls it left
to implementation.

## Warmth updates on dispatch, not completion

A worker becomes warm on a job's `CacheKeys` the moment the scheduler enacts
the job's `Dispatch` decision (`scheduler.go`'s `enactDispatch`), not when the
job later completes. "Warm on a job's keys" models a worker whose image
layers / build cache are primed by having *run* that job — the moment it
starts running is the moment that priming exists, and using dispatch keeps
the update synchronous with the decision that caused it (no need to thread
`CacheKeys` through `JobCompleted`, which doesn't carry them). Warmth is
merged and deduplicated per worker, sorted before storing, so the scheduler's
own state never depends on map iteration order.

## No intra-batch warmth propagation

A single `Schedule` call can decide several jobs at once (every event that
changes schedulability re-runs the policy over all currently-pending jobs).
`PriorityAffinity` reads `Worker.WarmCache` strictly as reported by the
`State` snapshot it's given — it does not simulate "this worker would be warm
if I placed job A on it, so job B (decided later in the same call) should see
that." Reasons: the `Policy` interface is pure and stateless by design
(no I/O, no mutation of what it's handed); warmth is scheduler-tracked state
established when a job *actually* dispatches, and simulating it inside the
policy would let a policy's internal bookkeeping silently diverge from the
scheduler's real state. The practical effect: on the burst scenario, most
affinity reuse happens across `Schedule` calls (as workers free up over time
and warm on the jobs they've run), not within the first big batch — which is
enough to show a real hit-rate and p99 improvement (see `bench/compare_test.go`).

## Pure priority for v1 — no aging

Per the spec's explicit recommendation: strict priority ordering, no aging.
A bounded set of high-priority jobs can and does delay low-priority ones
while it's in flight, but does not starve them permanently —
`TestPriorityAffinityNoStarvationUnderBoundedHighPriorityLoad`
(`scheduler/priority_affinity_test.go`) proves low-priority jobs dispatch
once the bounded high-priority set drains. Starvation-freedom under
*unbounded* high-priority arrival is explicitly out of scope (aging is a
separate, flagged refinement per the spec).

## Hold `Factors` carry only `no_capacity`

`PriorityAffinity` is work-conserving, so every `Hold` it emits is genuinely
"nothing fits anywhere" — unlike FIFO's declared head-of-line reservation
(ADR-0004). Its `Hold` `Factors` therefore carry only `{"no_capacity": 1}`,
deliberately *not* `priority`: `bench.checkWorkConservation`'s
`declaresReservation` treats any Factors key other than `no_capacity` as a
declared reservation and skips checking that hold. Including `priority` on a
Hold — even though it's meaningful context — would make the checker silently
stop verifying holds for prioritized jobs, which is exactly the case that
most needs checking. `Dispatch` decisions have no such constraint and record
`priority`, `cache_affinity`, and `warm_keys_matched` (the latter two only
when the job has `CacheKeys`, since affinity is undefined for a job with
none).

## Two `checkWorkConservation` fixes, both found by this policy

FIFO's holds are always the terminal decision of a `Schedule` call (it
`break`s on the first miss), so `bench.checkWorkConservation` had never been
exercised against a policy that keeps going after a hold. `PriorityAffinity`
does — it's work-conserving, so it `continue`s past a job that doesn't fit
and keeps trying the rest — which surfaced two real gaps in the checker
itself, not in the policy:

1. **Same-call ordering.** A `Schedule` call can legitimately produce
   `Hold(high-priority job), ..., Dispatch(lower-priority job)`: the
   high-priority job didn't fit, a later, smaller one did. Emitted in that
   order, the checker (which walks the log index by index) couldn't yet see
   that the later dispatch had already claimed the capacity it was asking
   about, and flagged a false "unexplained hold". Fixed in
   `scheduler/priority_affinity.go` by emitting every `Dispatch` from a call
   before any `Hold` from that same call — a log-ordering change only; it
   doesn't affect which job goes where, since `enactDispatch` only reads
   `Outcome == Dispatch` entries and dispatches to different workers never
   interact.
2. **Same-instant ties across separate `Schedule` calls.** Two calls can
   share the exact same logical instant — e.g. a job's completion event and
   some other event both land at `t`. The earlier of the two genuinely runs
   *before* that completion is processed, so the real scheduler still shows
   the completing job occupying its worker at that instant, but the
   free-capacity reconstruction's strictly half-open window `[At,
   At+duration)` treated the boundary as already free for every decision at
   that `t`, including ones that causally precede the completion. Fixed in
   `bench/invariants.go` by making the window's upper bound inclusive
   (a dispatch stays active while `end == d.At`) for this reconstruction
   specifically — `CheckCapacityInvariant` keeps its existing half-open
   convention, since it only ever needs to reason about genuinely distinct
   instants, never same-instant ties. (The reconstruction was later
   rewritten from a per-hold rescan, `freeCapacityAt`, into
   `checkWorkConservation`'s single forward sweep for performance — see the
   F16 build-log entry — but this inclusive-boundary behavior is preserved.)
   Regression test:
   `TestWorkConservationBoundaryTieNotFlagged` (`bench/invariants_test.go`).

Both were found empirically, by reconstructing the real free-capacity state
inside `PriorityAffinity.Schedule` at the flagged instants and comparing it
against the checker's reconstruction from the log — the two disagreed, and
the scheduler's real state (verified against `enactDispatch`'s own capacity
assertions, which panic on a true violation) was the correct one both times.
