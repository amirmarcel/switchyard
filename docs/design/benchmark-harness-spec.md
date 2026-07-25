# Switchyard — Benchmark Harness (design spec)

**Purpose.** The benchmark harness is what turns Switchyard from "a scheduler" into
"a platform for evaluating scheduling policies." It replays a defined workload through
the sim backend under a chosen policy and emits **exact** latency percentiles,
throughput, and utilization, with the scheduler invariants checked on every run. Once
this exists, every future policy is measurable the moment it lands — the candidate
policy becomes something you *validate with this*, not just add.

Hand this to the implementation agent (Claude Code). Signatures are illustrative; the
**design decisions are fixed**. Read `docs/design/scheduler-seam-spec.md` for the core
interfaces this builds on, and `CLAUDE.md` for the standing rules.

This should produce **ADR-0002** (why exact percentiles from raw samples, not bucketed
histograms) and a design doc alongside the seam spec.

---

## The one decision that matters most: measurement path ≠ operational path

There are two different observability needs, and conflating them produces indefensible
numbers. This slice builds the **measurement path only**:

- **Measurement (this slice):** the benchmark records every job's raw latency sample and
  computes **exact** percentiles (p50/p95/p99) by sorting, or via an HdrHistogram. This
  is what backs any "p99 improved X%" claim. It must be exact and reproducible.
- **Operational (NOT this slice):** Prometheus/Grafana live dashboards, OTel traces.
  Deferred. Do not add Prometheus here. Bucketed histograms give *approximate* quantiles
  and must never be the source of a headline benchmark number.

Why this matters: your README's benchmark methodology promises exact percentiles across
multiple runs. Prometheus can't honestly back that. So the harness owns measurement
directly. Record this in ADR-0002.

---

## Scope of this slice

**In:**
- A workload representation the harness can replay deterministically.
- A benchmark runner that drives a workload through the **sim backend** under a given policy.
- Exact metric computation: latency percentiles, throughput, worker utilization.
- Structured result output (JSON) + a human-readable summary.
- One canonical scenario definition to run.
- Invariant checking wired into the run (a violated invariant fails the benchmark).

**Out (do NOT build in this slice):**
- Prometheus, Grafana, OTel — the operational path. Deferred.
- The real Docker executor path for benchmarks — sim only for now (the sim is the
  scaling backend; the real executor is validated separately at small N).
- A second scheduling policy — benchmark runs against FIFO. The candidate policy is the
  *next* slice and will reuse this harness unchanged.
- The CLI (`main.go` / `cmd/`) — a `_test.go`-driven or minimal programmatic entrypoint
  is fine for now; the CLI graduates later.
- Multiple scenarios — one is enough to prove the harness. More are additive.
- Statistical A/B comparison tooling (benchstat-style) — comes with the second policy,
  when there are two things to compare.

---

## Workload representation

The harness replays a **fixed, seeded** workload so runs are comparable. Decision to make
explicit in the ADR/design doc: workloads are **generated from a seed + profile**, then
replayed identically — not regenerated fresh per run. Same seed ⇒ same workload ⇒
comparable runs. (This is the replayability point from the seam spec's determinism rules.)

Illustrative shape:

```go
// A workload is an ordered set of timed job submissions plus the worker pool it runs on.
type Workload struct {
    Name    string
    Seed    int64
    Workers []api.Worker
    Jobs    []TimedJob   // sorted by SubmitAt
}

type TimedJob struct {
    Job      api.Job
    SubmitAt api.Time
}
```

For this slice, a **single profile generator** is enough — a simple parameterized
generator (job count, arrival pattern, size distribution, worker count/capacity) seeded
by `Seed`. The three named profiles (interactive / batch / agent) from the README are
**not** required here; one representative generator that produces a burst is sufficient to
prove the harness. The profiles land with the workload-model slice later.

---

## The benchmark runner

```go
// Result is the exact, structured outcome of one benchmark run.
type Result struct {
    Workload   string
    Policy     string
    Seed       int64

    Jobs       int
    Completed  int
    Rejected   int            // admission rejections (Reject decisions)

    // Exact percentiles computed from raw samples — NOT bucketed.
    QueueDelayP50 api.Time
    QueueDelayP95 api.Time
    QueueDelayP99 api.Time
    // (queue delay = dispatch time - submit time; the metric the scheduler controls)

    ThroughputPerTick float64  // completions per unit logical time
    WorkerUtilization float64  // busy-worker-time / total-worker-time

    InvariantsHeld bool
    Violations     []string    // empty when InvariantsHeld
}

// Run drives one workload through the sim backend under one policy and returns Result.
// It records every job's raw queue-delay sample and computes exact percentiles.
func Run(w Workload, p api.Policy) Result
```

Key requirements:

- **Exact percentiles.** Collect the full slice of per-job queue-delay samples; compute
  p50/p95/p99 by sorting (or HdrHistogram). No bucketing. If you use HdrHistogram, note
  it in the ADR; for v1 a sorted-sample computation is simplest and exact.
- **Queue delay is the headline metric.** It's what the scheduler actually controls
  (submit → dispatch). Execution time is workload-modeled, not a scheduling outcome, so
  it's not the primary number.
- **Runs on the sim backend** via the existing sim driver + sim executor + logical clock.
  Reuse them; do not fork the scheduler.
- **Invariants checked.** After the run, verify the scheduler invariants over the decision
  log (every job eventually scheduled-or-rejected, capacity never exceeded, at-most-once,
  determinism). A violation sets `InvariantsHeld = false` and fails the benchmark.

---

## Methodology (must be enforced, not just documented)

The harness must make the README's benchmark methodology real:

- **Multiple runs:** the benchmark entrypoint runs each configuration **≥ 5 times** and
  reports the **median** result plus the spread, never a single run.
- **Warm-up excluded:** if the scenario has a ramp, exclude the warm-up window from the
  reported percentiles (document how).
- **Fixed seed, replayed workload:** identical workload across the runs being compared.
- **Determinism guard:** assert that repeated runs with the same seed produce identical
  decision logs (you already have this invariant — reuse it here as a precondition).

---

## Output

Two forms, both from the same `Result`:

- **Structured:** write `Result` (or the median across runs) as JSON to a known path
  (e.g. `bench/results/<scenario>.json`). This is what a plotting script (Python, later)
  consumes to make the before/after graphs.
- **Human-readable:** print a short summary to stdout — scenario, policy, jobs,
  completed/rejected, p50/p95/p99 queue delay, throughput, utilization, invariants held.
  This is the "runs and shows a number" experience.

Do not build plotting in this slice — JSON out is the contract; graphs come later.

---

## The canonical scenario

One scenario is enough to prove the harness. Define a **burst**: a worker pool of fixed
size, and a workload where a large batch of jobs arrives in a short window (the thing that
stresses a scheduler). Parameters live in code or a small config; the point is that it's
**named, seeded, and replayable**, so the same scenario can later be run against the
candidate policy for a direct comparison.

Name it descriptively (e.g. `burst`). It becomes the first entry in what will grow into a
`bench/scenarios/` set.

---

## Definition of "done" for this slice

- [ ] A seeded workload generator produces a replayable burst workload.
- [ ] `Run` drives it through the sim backend under FIFO and returns a `Result` with
      **exact** p50/p95/p99 queue delay, throughput, and utilization.
- [ ] The benchmark runs each config **≥ 5 times** and reports the median + spread.
- [ ] Invariants are checked per run; a violation fails the benchmark.
- [ ] Determinism is asserted (same seed ⇒ identical decision log) as a precondition.
- [ ] Results are written as JSON **and** printed as a readable summary.
- [ ] ADR-0002 records: exact-percentiles-from-raw-samples over bucketed histograms, and
      replay-not-regenerate for workloads.
- [ ] Tests cover: the generator is deterministic for a fixed seed; percentile math is
      correct on a known sample set; a deliberately capacity-starved run reports the
      right rejection/utilization numbers.

## Explicitly NOT done here

Prometheus/Grafana/OTel, the real-executor benchmark path, a second policy, the three
workload profiles, the CLI, plotting, and multi-scenario suites. Each is a later slice
that reuses this harness unchanged.

---

## Package placement

A new `bench/` package (created only now that it has code). It depends inward on `api/`,
`scheduler/`, and `simulation/` — never the reverse. Keep the generator, runner, metrics,
and scenario in cohesive files; do not pre-split into subpackages.

## First task to hand Claude Code

> Read `docs/design/benchmark-harness-spec.md` and implement the benchmark harness slice
> defined there, in a new `bench/` package. Build: a seeded, replayable burst workload
> generator; a `Run` function that drives it through the existing sim backend under the
> FIFO policy and computes EXACT p50/p95/p99 queue-delay percentiles (from raw samples,
> not bucketed), plus throughput and worker utilization; a ≥5-run harness reporting the
> median and spread; per-run invariant checking that fails on violation; JSON output plus
> a readable stdout summary; and one named `burst` scenario. Write ADR-0002 recording the
> exact-percentiles and replay-not-regenerate decisions. Add tests for generator
> determinism, percentile correctness on a known sample, and a capacity-starved run.
> Do NOT add Prometheus/OTel, a second policy, the real-executor path, the workload
> profiles, a CLI, or plotting — those are later slices. Report what you built and any
> design choices you made.
