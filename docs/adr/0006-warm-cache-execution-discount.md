# ADR-0006: Warm-cache execution-time discount, graded by key overlap

- Status: accepted
- Date: 2026-08-01

## Context

`docs/design/priority-affinity-notes.md` and ADR-0003 built a warmth model
(`Worker.WarmCache`) and an affinity-aware policy (`PriorityAffinity`) that
prefers dispatching a job to a worker already warm on its `CacheKeys`. But
the sim's duration model (`simulation.Executor`) always used one fixed
`JobDuration`, regardless of which worker a job landed on or how warm it
was. Cache affinity therefore only ever paid off as *placement* — a warm
worker was picked sooner — never as *execution speed*. On the burst
scenario (`bench/compare_test.go`), this meant priority+affinity showed a
real p50 win (jobs queue behind less contention) but a flat p99: nothing in
the model made a warm dispatch actually run faster, so the tail — the thing
cache affinity is supposed to matter most for — never moved. That
understates the policy's real value and left the benchmark unable to
demonstrate the thing ADR-0003 argued affinity was for.

## Decision

The simulator's duration model now applies a warm-cache execution-time
discount, **graded by cache-key overlap** rather than a flat
warm/cold multiplier:

- When a job dispatches to a worker, the fraction of the job's `CacheKeys`
  the worker was *already* warm on (before this dispatch) is computed —
  `warm_overlap` — and reduces the job's base duration proportionally. No
  overlap: no discount. Full overlap: the maximum discount. Partial
  overlap: a proportional discount in between.
- The maximum discount is capped at **50%** (`simulation.MaxWarmDiscount =
  0.5`): `duration = base * (1 - warm_overlap * 0.5)`. A fully warm job
  never runs in under half its cold duration.
- This lives entirely in the **execution model** (`simulation/warmcache.go`,
  applied in `simulation.Executor.Dispatch`), not in either `Policy`.
  `FIFO` and `PriorityAffinity` are unchanged — neither knows or cares that
  duration now varies; they only ever reasoned about placement. Execution
  time is a property of *what actually ran where*, not of which policy
  chose it, so a job dispatched to a coincidentally-warm worker under FIFO
  gets the same discount a `PriorityAffinity` dispatch would.
- Computing `warm_overlap` needs the worker's warmth *before* this
  dispatch's own keys merge into it (see the priority-affinity notes on
  warmth updating synchronously at dispatch) — that pre-merge state only
  exists momentarily inside `scheduler.enactDispatch`, which the `Executor`
  never sees directly (`api.Decision` carries job/worker IDs, not their
  full state). `enactDispatch` computes it once, uniformly, for every
  policy's dispatches, and logs it as `Decision.Factors["warm_overlap"]` —
  reusing the existing `Factors` mechanism `PriorityAffinity` already uses
  for `cache_affinity`, rather than growing the `Decision` struct's fixed
  fields. This is scheduler-core plumbing, not a policy change: it doesn't
  alter which job goes where, only what gets logged about a dispatch that
  already happened.
- Deterministic by construction: `DiscountedDuration` is a pure function of
  `(base, warm_overlap)`, and `warm_overlap` itself is derived from
  scheduler state that is already required to be deterministic (no
  wall-clock, no unseeded randomness). Same workload + seed still produces
  a byte-identical decision log, and now also byte-identical durations.

## Consequences

- The FIFO-vs-priority+affinity comparison now shows a real p99 movement,
  not just a p50 one: on the shipped burst scenario, p99 queue delay moved
  from a ~0.0% delta (flat, pre-discount) to an ~8.9% reduction for
  priority+affinity over FIFO (see `bench/compare_test.go`'s logged output;
  exact numbers depend on the workload/seed and will drift as the scenario
  changes, so the comparison test — not this document — is the source of
  truth going forward). This is the payoff cache affinity was always
  supposed to demonstrate: faster completions free capacity sooner, which
  lowers queue delay for jobs still waiting, compounding the placement win
  already measured.
- `bench`'s invariant checkers (`CheckCapacityInvariant`,
  `checkWorkConservation`) and `computeResult`'s makespan/utilization math
  all previously assumed every dispatch shared one fixed `w.JobDuration`.
  They now derive each dispatch's actual duration from
  `Factors["warm_overlap"]` via the same `DiscountedDuration` function the
  executor uses (`bench/invariants.go`'s `dispatchDuration`), so the
  measurement path and the execution path can never silently diverge.
  `checkWorkConservation`'s active-dispatch tracking, which relied on
  insertion order implying end-time order (true only when duration was
  uniform), was changed to a min-heap ordered by end time to stay correct
  under variable durations.
- No scheduling decision changes: capacity accounting, at-most-once
  fencing, work-conservation, and determinism are all evaluated exactly as
  before — only the wall/logical-clock duration a `JobCompleted` event
  fires at changes. This was confirmed by rerunning the full test suite
  (including the existing determinism and work-conservation tests) under
  `-race` with no changes to their expected outcomes.
- Warmth itself is unchanged: still merged, deduplicated, dispatch-time,
  no decay or eviction (`docs/design/priority-affinity-notes.md`). This ADR
  only changes what warmth *causes* at dispatch time, not how it's tracked.

## Alternatives considered

- **Flat warm/cold discount (binary: any overlap gets the full discount).**
  Rejected: collapses "warm on 1 of 8 keys" and "warm on 8 of 8" into the
  same execution time, which both overstates a marginal cache hit and
  doesn't reward `PriorityAffinity`'s actual placement quality (it already
  optimizes for *more* matched keys, per `bestAffinityFit` — a flat
  discount would make that optimization pointless for execution time, only
  useful for placement order).
- **No maximum-discount cap (unbounded reduction toward zero).** Rejected:
  a job that runs in near-zero time under a full match isn't a credible
  execution-time model for any real cache-affinity payoff (image layers
  still need to be attached, dependencies re-resolved, etc.) and would
  make the benchmark's throughput/utilization numbers on a fully-warm
  scenario look implausible. 50% was chosen as a round, defensible number
  representing "cache reuse meaningfully speeds up a job without pretending
  the job becomes instantaneous"; it is a modeling parameter, not a
  measured constant, and can be revisited if a future workload profile
  needs a different one.
- **Compute the discount inside `PriorityAffinity` and pass a precomputed
  duration through the `Decision`.** Rejected: this makes execution time a
  policy concern, contradicting the framing that motivated this slice
  (queue delay is what the scheduler controls; execution time is
  workload/environment-modeled, per `docs/design/benchmark-harness-spec.md`).
  It would also mean FIFO dispatches never got a discount even when they
  land on a warm worker by chance, which is physically wrong — warmth is a
  worker property, not a policy-reported one.
- **Grow `api.Decision` with dedicated `JobCacheKeys`/`WorkerWarmCache`
  fields instead of reusing `Factors`.** Rejected: `Factors` already exists
  precisely for "extra scored inputs to a decision"
  (`PriorityAffinity`'s `cache_affinity`/`warm_keys_matched`), and adding
  new top-level fields to the logged, replayed `Decision` type is a wider,
  more permanent surface change for the same information.
