# Switchyard

> *Working name — see Open Decisions. A railroad switchyard is where incoming traffic is sorted, prioritized, and routed onto a constrained set of tracks. That is exactly what a CI scheduler does with jobs and workers.*

**A platform for evaluating CI scheduling policies — through real execution and reproducible simulation.**

> Status: early development. v1 in progress.

---

## What this is, in one paragraph

Switchyard is a **platform for evaluating CI scheduling policies**. The scheduler itself is the *mechanism*; the benchmark harness is the *product*; the simulator and replay engine are the *method*. The design that makes this work: **one scheduling core drives two interchangeable backends** — a *real executor* that runs jobs as Docker containers on a worker pool, and a *discrete-event simulator* that replays synthetic workloads at scales impractical to reproduce on production infrastructure. The real backend proves the system works; the simulator lets any policy be pushed to breaking point against varied traffic shapes and injected failures. Every claim a policy makes is backed by two things: real p50/p95/p99 measurements, *and* proof that the scheduler's correctness invariants still hold. Nothing here is asserted to be "better" without a benchmark and a preserved invariant behind it.

The intended reader is anyone who wants to understand or measure CI scheduling tradeoffs — engineers learning distributed scheduling, and reviewers on platform / DevEx / CI teams evaluating how the author reasons about infrastructure. It is explicitly *not* a production CI system (see Non-goals).

---

## The problem

CI is the iteration loop of software engineering. When it is slow, flaky, or expensive, every engineer pays that tax on every change. But the bottleneck is frequently **not raw compute** — it is *scheduling*: which job runs where, in what order, with what priority, on which worker, against which cache. Bad scheduling surfaces as queue latency, wasted compute, cache misses, and unfair starvation of one team's jobs while another's flood the queue.

Three forces make this hard to reason about:

1. **Scheduling policy is invisible.** Most CI systems bury the policy inside the platform. You cannot easily swap FIFO for weighted-fair-queueing and observe what happens to p99.
2. **Workloads vary in shape.** Steady interactive traffic, bulk nightly batches, and bursty AI-agent traffic each favor different policies — and a scheduler tuned for one degrades under another.
3. **You cannot experiment on production.** There is no safe way to fire hundreds of thousands of jobs at a CI system serving thousands of engineers to see whether a new policy holds up. So scheduling changes ship on intuition instead of evidence.

Switchyard exists to make scheduling policy something you can **change, observe, and measure** — in a system that runs real work but can also be pushed to breaking point in simulation.

---

## What this platform evaluates (goals)

- **Pluggable, comparable policies.** One `Scheduler` core hosting interchangeable policy modules, swapped by config. A FIFO **baseline** and at least one **candidate** policy, measured head to head.
- **Real execution (planned).** The scheduler core is designed to drive a real Docker executor identically to the simulator; only the in-memory `FakeExecutor` (wall-clock, no containers) exists today.
- **Reproducible scale.** The simulator replays the *same scheduling code* against workloads from tens to thousands of jobs today; scaling to hundreds of thousands is blocked on a known O(N²) snapshot cost in the scheduler core (known-issue F10).
- **Correctness, not just speed.** Every run is checked against the capacity, work-conservation, and at-most-once invariants below; automated checks for the others (cancellation, dependency-respect) are not yet implemented — a policy is only "better" if it is faster *and* still correct.
- **Multiple workload shapes as first-class inputs.**
- **Fault tolerance (planned).** Failure injection with measured recovery — not yet built; see Failure semantics.

---

## What this project explicitly does NOT solve (non-goals)

*Scope discipline is a feature. Each boundary is a decision worth defending out loud.*

- **Not a production CI system.** A reference/study platform, not a replacement for GitHub Actions, Buildkite, CircleCI, or Jenkins.
- **Does not own application test logic.** Switchyard *runs* jobs; it does not write, maintain, or judge the tests inside them. Test-logic flakiness is out of scope (infra-caused flakiness is a possible future direction, not v1).
- **Not a build system.** It orchestrates opaque jobs; it does not resolve source dependency graphs or do incremental compilation.
- **Not a remote build cache (v1).** Deferred.
- **Not a merge queue (v1).** Deferred.
- **Not an autoscaler or cost optimizer (v1).** Worker pools are simulated or locally provisioned. Deferred.
- **Not a general workflow engine.** Scoped to CI job orchestration, not arbitrary business workflows.
- **Not hardened for untrusted or multi-tenant execution.** Not a security sandbox for hostile code.
- **Not a many-algorithm bake-off.** Few policies, rigorously measured.

---

## Architecture

The load-bearing decision is that **the scheduler core is backend-agnostic.** The real executor and the simulator implement the same `Worker` / `Job` interfaces, so the *identical scheduling code path* runs whether jobs execute in Docker or advance through simulated time. This is what lets Switchyard answer two questions that usually trade off — *"does it actually work?"* and *"can you benchmark it at scale?"* — with a single "yes."

```
                        Job source  (webhook / synthetic workload generator)
                                    |
                          +---------v---------+
                          |   Orchestrator    |   intake, stage sequencing,
                          |                   |   retries, deadlines, cancellation
                          +---------+---------+
                                    |
                          +---------v---------+
                          |  Scheduler core   |   hosts a Policy module; emits a
                          | (backend-agnostic)|   first-class Decision per action
                          +----+---------+----+
                               |         |
              +----------------+         +----------------+
              |                                           |
     +--------v---------+                       +---------v--------+
     |  Real executor   |    same interface     |    Simulator     |
     |  (planned: Docker|<---------------------->| discrete-event   |
     |  workers, wall-  |                       |  logical time    |
     |  clock time)     |                       |                  |
     +--------+---------+                       +---------+--------+
              |                                           |
              +---------------------+---------------------+
                                    |
                          +---------v---------+
                          | Metrics + traces  |   (planned — stack
                          | + decision log    |   unconfirmed)
                          +---------+---------+
                                    |
                          +---------v---------+
                          | Benchmark reports |   percentiles, throughput,
                          | + invariant check |   utilization, recovery time
                          +-------------------+
```

**Time.** The real executor runs on wall-clock time; the simulator advances **logical time** — the clock jumps from event to event rather than sleeping — which is what makes replaying a hundred thousand jobs fast and deterministic. Same scheduler logic, two notions of "now."

### How a job moves through the system

```
submit → validate → enqueue → policy selects worker → dispatch
       → execute → heartbeat(s) → complete → metrics + decision emitted
```

Every transition is observable, and cancellation/failure can interrupt at any stage (see Failure semantics).

### Policy as a plugin

The scheduler does not "implement WFQ." It hosts interchangeable policy modules behind one interface — `Scheduler → Policy → Decision`. Adding a policy means adding a module, not editing the core. That framing is what makes the *platform*, not the scheduler, the point.

### The Decision is a first-class object

Every scheduling action emits a structured, logged `Decision` — conceptually:

```
Decision {
    JobID
    WorkerID          // or a "hold" / no-dispatch outcome
    PolicyName
    Factors           // e.g. cache affinity, fairness score, queue age, priority
    QueueDelay
    LogicalTimestamp
}
```

Making the Decision first-class is deliberate: **observability, replay, debugging, benchmarking, and policy-diffing all derive from the decision log.** Because decisions are recorded, you can replay two policies over the identical workload and explain *why* they diverged — not just that Policy A had lower p99, but that it dispatched job X to worker 3 for cache affinity where Policy B held it for fairness. That lets you evaluate **decision quality** (starvation avoidance, cache-affinity rate, fairness) alongside outcome metrics (latency, throughput).

*(Note: the Decision object is the internal mechanism, not the project's identity. The identity is the evaluation platform; decisions are how it works.)*

**Module layout** (folders are created as they fill, not upfront): `scheduler/`, `orchestrator/`, `executor/`, `simulation/`, `bench/`, `metrics/`, `chaos/`, `api/`, `cmd/`, `docs/adr/`.

---

## Scheduler invariants (correctness)

A scheduler is not successful because it is fast. It is successful because it is fast **while never violating correctness.** Capacity, work-conservation, and at-most-once are checked on every benchmark run today; the others are enforced by the scheduler core but not yet automated as run-time checks (see the caveats below). The goal is that the story becomes *"p95 improved while all invariants held,"* not just *"p95 improved."* They sit here — before the benchmark — because the benchmark measures how well the scheduler preserves them.

- Every submitted job is **eventually scheduled** (no permanent starvation).
- No job **executes more than once** (at-most-once completion), enforced by lease fencing: every dispatch mints a fresh lease, and a completion whose lease doesn't match the assignment's current lease is fenced (rejected) rather than applied — so a stale completion from a reassigned job cannot double-count.
- **Worker capacity is never exceeded.**
- **Job dependencies are respected** (a stage never starts before its predecessors finish). Enforced in the scheduler core, but not yet exercised by any test or scenario, and the work-conservation checker does not yet account for dependencies (known-issue F15).
- **Cancellation propagates** to queued work — a cancelled job is removed from the pending set before dispatch. Cancellation of in-flight work does not yet stop execution (planned; known-issue F8): the running job completes and its result is discarded.
- Scheduling is **deterministic** given the same workload and seed (prerequisite for replay).
- **Work-conservation (conditional):** no runnable job idles while capacity exists — *unless a policy deliberately reserves capacity* (e.g. holding a worker for an incoming high-priority job). Non-work-conservation is therefore a documented, measured policy property, never a silent scheduling gap. This tension — backfill vs. reservation — is itself one of the things the platform is built to measure.

---

## Failure semantics (planned)

No failure injection exists yet — no worker-kill chaos experiment, no lease expiry, no bounded retry. This section describes the intended design, not current behavior. Lease fencing (below) is already implemented; what's missing is the failure-injection harness around it. Remaining prerequisites: bounded/attributed retries on `JobFailed` (F6, which currently triggers unbounded, unattributed re-dispatch), and real cancellation of in-flight work (F8).

Intended design — chaos injection is only interesting if the recovery path is specified. Worker failure is lease-based:

```
worker dies → lease expires → job becomes runnable again
            → scheduler re-evaluates → a worker claims the lease
            → resume or restart
```

Lease **fencing** is implemented and upholds at-most-once: a completion whose lease doesn't match the assignment's current lease is rejected, so a briefly-partitioned worker cannot double-complete work that has already been rescheduled. What's still planned is the failure-injection half — a lease-*expiry* timer and worker-kill chaos experiment — so recovery time (lease expiry → re-dispatch → completion) is not yet a measured output.

---

## Workload model

Different policies win under different traffic. Switchyard makes the traffic explicit rather than hand-waving it, with **three canonical profiles**. These are **synthetic approximations**, not captured traces — the goal is realistic *shape*, and the assumptions are stated so they can be argued with.

| Dimension            | Interactive developers        | Nightly batch                 | AI agents                              |
|----------------------|-------------------------------|-------------------------------|----------------------------------------|
| Arrival              | steady, diurnal               | bulk spike at a fixed time    | bursty, highly correlated              |
| Volume               | low–moderate                  | high (all at once)            | very high                              |
| Job size / duration  | mixed, often larger           | large, long                   | small, short                           |
| Priority             | high (a person is waiting)    | low (no one waiting)          | low per-job                            |
| Dependency depth     | deeper stage graphs           | deep (full builds)            | shallow (lint/unit-only)               |
| Burstiness           | moderate                      | one large trigger spike       | high (a fleet fires near-simultaneously)|
| Cache locality       | moderate overlap              | low–moderate                  | high (near-duplicate jobs)             |

Three distinct shapes stress different policy properties: interactive traffic tests responsiveness under priority, nightly batch tests throughput and starvation avoidance, agent traffic tests behavior under correlated bursts and cache affinity. AI is one important case, not the centerpiece — which, if anything, makes the connection to modern CI teams feel more organic than a single tailored scenario would.

---

## Reference evaluation scenario

The canonical scenario mixes all three profiles under contention and asks the question a platform team actually cares about: **do interactive developers keep moving when a nightly batch and an agent burst hit at the same time?** The candidate policy of interest is one motivated by that contention — *prioritize interactive/human PRs under load*, or *deduplicate near-identical agent jobs*. The result to produce: interactive-PR p99 latency, candidate policy on vs. off, with all invariants still holding.

---

## Benchmark methodology

Credibility lives here. Every reported result follows the same protocol:

- **≥ 5 runs** per configuration; report the **median** and **percentiles**, never a single run or a bare average.
- **Warm-up discarded** — steady-state only *(planned — not yet implemented)*.
- **Fixed random seed** and **replayed** (not regenerated) workloads, so runs are comparable.
- **Identical worker pool** and environment across the configurations being compared.
- **Invariant check** runs alongside every benchmark; a violated invariant fails the run.

*Replay, not regeneration, is deliberate — see the replayability ADR.*

---

## Future experiments (questions Switchyard should answer)

- Does prioritizing interactive traffic reduce overall throughput, and by how much?
- What does fairness cost in latency?
- How much does cache affinity actually matter?
- Does the candidate policy still beat FIFO under burst traffic, or only at steady state?
- When does queue starvation first appear?
- How much does a single worker failure move p99?

---

## Threats to validity

Stated up front, both as intellectual honesty and to preempt the obvious objections:

- The simulator abstracts away **network jitter** and real I/O variance.
- **Docker startup cost differs from Kubernetes** pod scheduling; absolute provisioning numbers won't transfer.
- Workloads are **synthetic approximations**, not captured production traces.
- **Single-node execution** caps absolute throughput; large-N results are the simulator's, not the real executor's.
- The goal is **comparative evaluation** (policy A vs. B under identical conditions), **not absolute production numbers.**

---

## What "done" means for v1 (success criteria)

- [ ] A real multi-stage CI job (checkout → build → test) runs in Docker via the executor.
- [ ] The *same scheduler core* drives the simulator over a replayable scenario.
- [x] Baseline (FIFO) + one candidate policy, benchmarked per the methodology above.
- [ ] The **reference evaluation scenario** produces a defensible delta.
- [ ] **All scheduler invariants verified** to hold across every benchmarked run.
- [ ] One **chaos experiment** (worker killed mid-job) showing lease-based recovery, with recovery time measured.
- [ ] **6–8 ADRs** capturing the key design rationale.
- [ ] A **benchmark report** linked from this README, with before/after graphs. *(Add dashboard screenshots — queue depth, worker utilization, latency histogram, trace waterfall — once the observability layer renders them.)*

The finish bar matters more than the feature count. A complete, benchmarked v1 photographs as a capstone; a half-built multi-subsystem platform photographs as an unfinished experiment. Depth over breadth is the whole strategy.

---

## Design principles

- **Measure, don't assert.** Every "X beats Y" is backed by data that survives the methodology above.
- **Correctness gates performance.** A faster policy that breaks an invariant is not an improvement.
- **Real execution keeps you honest; simulation lets you scale.** Neither alone is enough.
- **Non-goals are load-bearing.** Knowing what not to build is part of the engineering.
- **Only running the system proves behavior.** Static structure proves well-formedness; the benchmark and the chaos run prove it holds up. *(Carried forward from the platform-zero thesis.)*

---

## Roadmap beyond v1 (capabilities — expansion, not prerequisites)

- **Merge queue** — speculative batch execution + automatic bisection.
- **Autoscaling** — spot-interruption handling and cost-aware scheduling onto a heterogeneous compute stack.
- **Remote build cache** — content-addressable storage, possibly speaking the Bazel Remote Execution API.
- **More policies** — DRF, shortest-remaining-processing-time.
- **Agent duplicate-work detection** — cache-affinity-aware dedup of near-identical jobs.

---

## Architecture Decision Records

Target 6–8 ADRs (4 written so far: 0001–0004; see `docs/adr/`). Seeds:

1. Why FIFO as the baseline first.
2. Why *this* candidate policy (e.g. WFQ or priority+affinity) rather than DRF for v1.
3. Why a real executor *and* a discrete-event simulator (the dual-backend decision).
4. Why Docker directly instead of a Kubernetes layer for v1.
5. Why model three workload profiles as first-class inputs.
6. Why the Decision is a first-class, logged object.
7. **Why replayable workloads instead of fresh random generation** — reproducibility and fair A/B comparison depend on it.
8. Why lease-based failure recovery (and how fencing upholds at-most-once).

---

## Open decisions to lock before writing code

- [ ] **Name.** Switchyard is a placeholder.
- [x] **The candidate scheduling policy for v1** — priority+affinity, chosen over WFQ (ADR-0003).
- [x] **The contention-motivated policy** — interactive prioritization, resolved for free by priority+affinity (ADR-0003).
- [ ] **Docker-only vs. a thin k8s layer for v1** — recommendation: Docker for v1, k8s as expansion.
- [ ] **Metrics stack** — Prometheus + OpenTelemetry assumed; confirm.
- [ ] **How realistic the checkout → build → test job needs to be.**
