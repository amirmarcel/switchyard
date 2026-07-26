# Switchyard — Candidate Policy: Priority + Cache Affinity (design spec)

**Purpose.** Add the second scheduling policy so the platform can *compare* policies. FIFO
is the baseline; priority+affinity is the candidate. Because the benchmark harness already
exists, this policy is measurable against FIFO the moment it lands — that comparison is the
whole payoff. See ADR-0003 for why this policy was chosen over WFQ.

Hand this to Claude Code. Signatures illustrative; **design decisions fixed**. Read
`docs/design/scheduler-seam-spec.md` (the `Policy` interface this implements),
`docs/design/benchmark-harness-spec.md` (how it gets measured), and `CLAUDE.md`.

Produces **ADR-0003** (already decided — the policy choice) plus, if any non-obvious
modeling call is made (e.g. how warmth updates), a short design note.

## The policy, precisely

Priority + cache affinity is one `Policy` implementation with two mechanisms:

1. **Priority:** among runnable jobs, consider higher-priority jobs first. (`Job.Priority`
   already exists.)
2. **Affinity:** for a given job, among workers with free capacity, prefer one already
   *warm* on the job's cache keys over a cold one.

It implements the same `Policy` interface as FIFO — `Schedule(State, now) []Decision` —
and must obey the same rules: **pure, deterministic, no wall-clock, no I/O, no blocking,
seeded/ordered only.** FIFO is not modified; this is a new, parallel policy.

## What this slice adds to the model

Two small model additions, both reused later by the workload profiles:

```go
// On Job (api/): the cache keys this job would benefit from a worker being warm on.
CacheKeys []string

// On Worker (api/): the cache keys this worker is currently warm on.
// (How warmth is established/updated is defined below.)
WarmKeys []string
```

**Warmth model (make this decision explicit, keep it simple for v1):** a worker becomes
warm on a job's `CacheKeys` when it runs that job, and stays warm on them (no eviction/
decay in v1 — decay is a later refinement). Warmth is state the scheduler tracks per
worker, updated when a job is dispatched/completed. Keep it deterministic (ordered
updates). If you introduce any decay or capacity limit, that's a separate decision —
don't; v1 is "warm forever once run."

## The scheduling logic

```
Schedule(state, now):
  order runnable jobs by (priority desc, then submit time asc, then JobID for determinism)
  for each job in that order:
      candidates = workers with free capacity >= job.Needs
      if none: Hold(job, reason="no_capacity")   // same as FIFO
      else:
          pick the candidate that is warm on the most of job.CacheKeys
          (tie-break deterministically: e.g. most-warm, then worker ID)
          Dispatch(job, worker) with Factors recording the affinity score
```

Key requirements:

- **Priority ordering** replaces FIFO's pure submit-order. Ties broken by submit time then
  JobID — deterministic total order (determinism invariant).
- **Affinity is placement, not admission.** It only chooses *which* viable worker; it never
  holds a job that could otherwise run just to wait for a warm worker — UNLESS you
  deliberately implement capacity reservation, which for v1 you should NOT (keep it
  work-conserving; non-work-conservation is a later, explicitly-flagged policy property).
- **Decisions record affinity in `Factors`.** e.g. `{"priority": 5, "cache_affinity": 0.75,
  "warm_keys_matched": 3}`. This is what makes "why did affinity place job X on worker Y"
  answerable from the decision log, and what lets a benchmark measure *decision quality*
  (affinity hit-rate) not just latency.
- **Admission unchanged.** Unplaceable jobs (needs exceed max worker) are still rejected at
  admission (ADR-0001) — that logic is policy-independent and already lives in the
  scheduler, not the policy. Don't duplicate it here.

## Invariants — all still hold

This policy must satisfy every existing invariant, verified by the same checks the harness
already runs: eventually-scheduled (no starvation — priority must not permanently starve
low-priority jobs; see the test below), at-most-once, capacity never exceeded, dependencies
respected, deterministic replay, conditional work-conservation. **A faster policy that
breaks an invariant is not an improvement.**

The starvation risk is real and specific: strict priority can starve a low-priority job if
higher-priority jobs keep arriving. For v1, document whether you accept this (pure priority)
or mitigate it (e.g. aging). **Recommendation: pure priority for v1, plus a test asserting
that with a bounded set of high-priority jobs, low-priority jobs DO eventually schedule
once the high-priority ones drain.** If you later want starvation-freedom under unbounded
high-priority load, that's aging — a separate, flagged refinement, not v1.

## The comparison this enables (the point of the slice)

Once this lands, the benchmark harness runs the **same `burst` scenario** under FIFO and
under priority+affinity and produces a direct comparison. Extend the scenario/workload so
jobs carry `CacheKeys` with meaningful overlap (so affinity has something to exploit) and
priorities (so priority ordering matters). The headline result to produce:

> Under a high-cache-overlap burst, priority+affinity reduced p99 queue delay by X% vs FIFO
> (affinity reused warm workers), while all invariants held.

Add priorities such that a small set of high-priority jobs also demonstrates priority
ordering (their p99 is lower than the rest). This is the reference-scenario seed —
interactive prioritization falls out of the priority mechanism, per ADR-0003.

## Definition of "done"

- [ ] `Job.CacheKeys` and `Worker.WarmKeys` added to `api/`.
- [ ] A deterministic warmth model (warm-on-run, no decay in v1) tracked by the scheduler.
- [ ] A new `PriorityAffinity` policy implementing `Policy`, pure and deterministic,
      obeying priority ordering + affinity placement + work-conservation.
- [ ] FIFO untouched; both policies selectable.
- [ ] Decisions record priority + affinity in `Factors`.
- [ ] All existing invariants verified under the new policy (reuse the harness checks).
- [ ] A no-starvation test: bounded high-priority load ⇒ low-priority jobs eventually run.
- [ ] A determinism test: same workload+seed ⇒ identical decision log under the new policy.
- [ ] An affinity test: given warm vs cold workers, the policy places on the warm one.
- [ ] The `burst` scenario extended with cache keys + priorities so the comparison is
      meaningful; a benchmark run comparing FIFO vs priority+affinity, with the p99 delta
      and affinity hit-rate reported, and invariants held under both.
- [ ] Build-log entry; ADR-0003 already written (commit it with this slice).

## Explicitly NOT in this slice

- No aging / starvation-freedom under unbounded priority load (later refinement).
- No warmth decay or cache eviction (v1 is warm-forever-once-run).
- No WFQ, no DRF, no dedup (separate future policies/mechanisms).
- No real Docker executor path (sim benchmark, as before).
- No Prometheus/OTel, no CLI (deferred slices).
- No statistical A/B tooling beyond the harness's existing median+spread — though now that
  there are two policies to compare, a benchstat-style significance comparison becomes a
  reasonable *next* addition (not this slice).

## Package placement

The policy lives in `scheduler/` alongside `fifo.go` (e.g. `scheduler/priority_affinity.go`)
— same package, implementing the same interface. The model fields go in `api/`. Do not
create new packages.

## First task to hand Claude Code

> Read `docs/design/candidate-policy-spec.md` and ADR-0003, and implement the
> priority+affinity candidate policy. Add `Job.CacheKeys` and `Worker.WarmKeys` to api/; a
> deterministic warm-on-run warmth model (no decay in v1) tracked by the scheduler; a new
> `PriorityAffinity` policy in scheduler/ implementing the existing `Policy` interface —
> pure and deterministic, ordering runnable jobs by (priority desc, submit asc, JobID) and
> placing each on the free-capacity worker warmest on its cache keys, work-conserving (no
> capacity reservation), recording priority + affinity in the decision `Factors`. Leave FIFO
> and the admission check untouched. Extend the `burst` scenario/workload with cache keys
> (meaningful overlap) and priorities so the comparison is meaningful, and produce a
> benchmark run comparing FIFO vs priority+affinity (report the p99 delta and affinity
> hit-rate). Add tests: no-starvation under bounded high-priority load, determinism under a
> fixed seed, and affinity placement (warm worker preferred). All existing invariants must
> hold under the new policy. Do NOT add aging, warmth decay, WFQ/DRF/dedup, the real
> executor path, Prometheus, or a CLI. Report what you built and any modeling choices.
