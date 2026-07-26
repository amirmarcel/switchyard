# ADR-0003: Candidate policy is priority + cache affinity

- Status: accepted
- Date: 2026-07-25

## Context

FIFO is the baseline policy. To make Switchyard a platform that *compares* policies
(not just runs one), it needs a second, non-trivial policy to measure FIFO against, now
that the benchmark harness (ADR-0002) exists to measure it. Two candidates were weighed:

- **Priority + cache affinity:** dispatch higher-priority jobs first; among viable
  workers, prefer one already warm on the job's cache keys.
- **Weighted Fair Queueing (WFQ):** allocate worker-time across queues/tenants in
  proportion to weights, via virtual-time accounting.

## Decision

The candidate policy is **priority + cache affinity**.

Rationale:
- **Lower implementation risk.** It builds directly on the FIFO scaffolding; the only new
  surface is a cache-key model on jobs and a warmth model on workers. WFQ requires
  virtual-time accounting and a higher correctness bar (proving convergence to weighted
  shares), which is a larger, riskier build.
- **More legible, more on-thesis benchmark result.** "Affinity-aware placement reused warm
  workers and cut p99 under a high-cache-overlap burst" is immediately understandable and
  is exactly the win an AI-agent workload (many near-identical jobs) makes dramatic —
  matching the platform's stated direction. WFQ's fairness result is a stronger pure
  distributed-systems flex but less CI-native.
- **Compounds with work already planned.** Cache locality is a dimension of the three
  workload profiles (interactive/batch/agent) this project needs regardless, so building
  affinity now *is* building workload-model infrastructure, not a detour.
- **Resolves the contention-policy decision for free.** The reference evaluation scenario
  ("do interactive developers keep moving under a mixed burst?") needs an interactive-
  prioritization mechanism. Priority is already part of this policy, so interactive
  prioritization falls out of it — no separate mechanism to build. (Duplicate-work
  detection, the alternative contention mechanism, is a separate detection system and
  stays deferred to a later slice.)

## Consequences

- FIFO vs. priority+affinity becomes the first real policy comparison the benchmark
  harness measures. The candidate policy drops into the existing harness unchanged.
- Jobs gain `CacheKeys`; workers gain a warmth model (which keys they are warm on, and how
  warmth updates as jobs run). This model is reused by the workload profiles later.
- The scheduler's placement path gains affinity scoring (prefer warm workers) rather than
  first-fit. FIFO is untouched — it remains the baseline.
- Interactive prioritization for the reference scenario is now covered by this policy's
  priority ordering; no separate contention mechanism is needed for v1.
- Non-work-conservation stays governed by the existing conditional invariant: if the
  policy ever holds a viable worker to reserve capacity for a higher-priority job, that is
  a logged, deliberate decision, not a violation.

## Alternatives considered

- **WFQ as the v1 candidate.** Rejected for now: higher implementation and correctness
  risk, a less CI-legible benchmark, a tenant model that does not compound with the
  workload work, and it would leave the contention-policy decision unresolved. WFQ is a
  strong *next* candidate policy after affinity — a platform comparing FIFO vs. affinity
  vs. WFQ (and later DRF) is a better story than any one policy — so it is deferred, not
  discarded.
- **Duplicate-work detection as the contention mechanism.** Rejected as the v1 answer:
  it is a separate detection system (content hashing / cache-key equality), not a
  scheduling policy, and interactive prioritization (free with priority+affinity) already
  answers the reference scenario. Dedup remains a roadmap item.
