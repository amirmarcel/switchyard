# ADR-0002: Exact percentiles from raw samples, and replay-not-regenerate workloads

- Status: accepted
- Date: 2026-07-25

## Context

The benchmark harness (`bench/`, `docs/design/benchmark-harness-spec.md`) exists to back
claims like "p99 queue delay improved X%" once a second scheduling policy lands. That
claim is only defensible if the number behind it is reproducible and exact, and if the
workload it was measured against is the same workload a competing policy will later be
measured against.

Two design decisions were called out as fixed in the spec and needed to be recorded
before implementation: how percentiles are computed, and whether a workload is
regenerated per run or generated once and replayed.

## Decision

**Percentiles are computed exactly, from the full sorted slice of raw per-job queue-delay
samples — never from a bucketed histogram.** `bench.Run` collects every dispatched job's
`QueueDelay` (already computed by the scheduler as dispatch time − submit time, see
`api.Decision`), sorts it, and computes p50/p95/p99 by the nearest-rank method:
`rank = ceil(p/100 * n)`, 1-indexed into the sorted slice. No interpolation between
samples, no bucket boundaries. This is what `bench/metrics.go`'s `percentile` function
does.

**Workloads are generated once from a seed and replayed identically across every run
that references them, never regenerated fresh per run.** `bench.GenerateBurst` is a pure
function of its seed and params, using a locally-seeded `math/rand` source (never the
global `rand`, matching the scheduler's own determinism constraints in `CLAUDE.md`) —
same seed and params always produce a byte-identical `Workload`. The ≥5-run harness
(`bench.RunHarness`) takes one `Workload` value and drives it through the sim backend
five-plus times unchanged; it does not call the generator again between runs.

## Consequences

- **Exact percentiles are reproducible bit-for-bit** given the same decision log, which
  makes the determinism guard (`RunHarness` comparing decision logs across repeated
  runs, byte-identical or the run is flagged non-deterministic) meaningful as a
  precondition for trusting the reported numbers at all.
- **A workload is a stable point of comparison.** Once the candidate policy lands, it
  reuses the exact same `Workload` value (same seed, same jobs, same arrival times) that
  FIFO was measured against, so "policy A vs policy B" is an apples-to-apples comparison
  on identical input — not two different random draws that happen to share a seed
  parameter.
- **The nearest-rank method was chosen over interpolated percentiles** (e.g. linear
  interpolation between the two nearest ranks) for simplicity and because it always
  returns an observed sample value rather than a synthesized one — there is no ambiguity
  about what p99 "means" when someone asks to see the actual job behind it.
- **Memory cost scales with sample count** (the full slice is kept and sorted, not
  reduced online). At this slice's scale (hundreds of jobs) this is a non-issue; if a
  future scenario needs orders of magnitude more samples, switching the sample store to
  an HdrHistogram is the natural next step — the spec anticipated this ("If you use
  HdrHistogram, note it in the ADR") but it isn't needed yet, so it isn't built.
- **This is the measurement path only.** Prometheus/OTel (the operational path) would
  give approximate, bucketed quantiles and must never be the source of a headline
  benchmark number — see the spec's "measurement path ≠ operational path" framing. That
  remains out of scope for this slice and is not touched by this decision.

## Alternatives considered

- **Linear-interpolated percentiles** (the method many stats libraries default to).
  Rejected for v1: nearest-rank is simpler to implement and audit, and always points at
  a real sample, which matters more for a benchmark whose job is to be trusted than for
  a general-purpose stats library. Revisit if a future consumer specifically needs
  interpolated percentiles for compatibility with another tool's output.
- **HdrHistogram from the start.** Rejected for this slice: at hundreds-to-low-thousands
  of samples per run, sorting a raw slice is exact, fast enough, and has no
  quantization error to reason about. HdrHistogram is the right call once sample counts
  grow large enough that exact sorting becomes the bottleneck — not before.
- **Regenerate the workload fresh for each of the ≥5 runs (same seed, called again).**
  Rejected: this makes the ≥5-run harness's determinism guard tautological (of course a
  pure generator returns the same jobs) instead of proving what actually matters —
  that the *scheduler* is deterministic when given the identical event stream. Replaying
  one generated `Workload` object through the sim backend N times is what actually
  exercises replay-determinism end to end.
- **Compute percentiles per-worker or per-job-class instead of one pool-wide figure.**
  Rejected as premature: the spec's scope is one policy, one scenario; a single headline
  queue-delay percentile is enough to prove the harness. Segmented percentiles are
  additive and can be layered on once there's a second policy or the workload profiles
  land.
