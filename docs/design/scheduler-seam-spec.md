# Switchyard — Core Interface & Backend Seam (design spec)

**Purpose.** This is the design for the seam the whole platform rests on: one scheduler core driving two backends (a real Docker executor on wall-clock time, and a discrete-event simulator on logical time). It is written to be handed to an implementation agent (Claude Code). The **signatures are illustrative** — names and error handling may be refined during implementation — but the **design decisions are fixed** and must be honored, because they are what make the dual-backend claim hold.

This document should become **ADR-0003** (dual-backend) plus its supporting design doc.

---

## The central decision: event-driven, single-threaded, time-agnostic

The scheduler core is a **single-threaded event loop**. It:

- consumes a unified stream of `Event`s,
- reads time only through a `Clock` interface (never `time.Now()`, never `time.Sleep`),
- asks the `Policy` for `Decision`s when scheduling state may have changed,
- enacts those decisions through an `Executor`,
- and never blocks.

Why not synchronous/blocking dispatch? It breaks both backends. The real executor would block a goroutine per running job, so completions arrive concurrently — reintroducing races and destroying determinism. The simulator can't block at all; its job is to advance logical time, not wait. Event-driven dispatch removes the conflict: dispatch is non-blocking, and completion returns *later* as an event.

**The backend's only job is to decide *when* a completion event fires.**

- **Docker executor (real):** runs the container in a goroutine; on exit, pushes a `JobCompleted` (or `JobFailed`) event onto the event stream. Time is wall-clock.
- **Sim executor (logical):** on dispatch, computes the job's modeled duration and *schedules* a `JobCompleted` event at `now + duration` in a time-ordered event queue. Time is logical and advances event-to-event.

Both push the **same event type** onto the **same loop**, feeding the **same policy**. The scheduler is identical across backends; only the event *source* and the *clock* differ.

```
            Events (submitted / completed / worker-free / lease-expired ...)
                                   |
                          +--------v---------+
                          |  Scheduler loop  |  single-threaded, Clock-driven
                          |  (Handle event)  |
                          +--------+---------+
                                   | asks
                          +--------v---------+
                          |      Policy      |  pure: (State, now) -> []Decision
                          +--------+---------+
                                   | returns Decisions
                          +--------v---------+
                          |     Executor     |  Dispatch(decision) — non-blocking
                          +--------+---------+
                          /                    \
             Docker executor                    Sim executor
      (goroutine; JobCompleted                 (schedules JobCompleted
       on container exit)                       at now+duration in event queue)
```

---

## Domain types

Illustrative Go. Treat IDs and time as distinct types so they can't be confused.

```go
type JobID string
type WorkerID string
type Time   int64        // nanoseconds; real clock = wall time, sim clock = logical time

type WorkloadClass int
const ( Interactive WorkloadClass = iota; Batch; Agent )

type Resources struct { CPUMillis int; MemBytes int64 }

type Job struct {
    ID           JobID
    Class        WorkloadClass
    Needs        Resources
    Priority     int          // higher = more urgent; interactive > batch/agent
    Deps         []JobID      // stage graph: all must complete before this is runnable
    CacheKeys    []string     // for affinity: workers warm on these keys are preferred
    SubmitAt     Time
    Timeout      Time         // deadline relative to dispatch; 0 = none
}

type Worker struct {
    ID        WorkerID
    Capacity  Resources
    WarmCache []string        // cache keys this worker is warm on (affinity signal)
    // liveness (lease/heartbeat) tracked by the scheduler, not carried here
}

type Assignment struct {
    Job      JobID
    Worker   WorkerID
    LeaseID  string           // fencing token; see Failure semantics
    StartAt  Time
}
```

### The Decision — first-class and logged

Every scheduling action emits one `Decision`, whether it dispatches or deliberately holds. This is the unit of observability, replay, and policy-diffing.

```go
type Outcome int
const ( Dispatch Outcome = iota; Hold )   // Hold = deliberately leave capacity idle

type Decision struct {
    Outcome   Outcome
    Job       JobID
    Worker    WorkerID          // set when Outcome == Dispatch
    Policy    string
    Factors   map[string]float64 // e.g. {"cache_affinity":1, "fairness":0.7, "queue_age_ms":420}
    QueueDelay Time              // now - job.SubmitAt at decision time
    At        Time              // logical or wall-clock timestamp
}
```

---

## Core interfaces

```go
// Clock — the scheduler and policy read time only from here.
type Clock interface { Now() Time }

// State — a read-only snapshot the policy reasons over. Ordering must be stable
// (deterministic) — no raw map iteration.
type State interface {
    Pending() []Job          // runnable jobs (deps satisfied), in stable order
    Workers() []Worker       // with free capacity + warm-cache info
    Running() []Assignment   // for context (fairness, affinity)
    FreeCapacity(WorkerID) Resources
}

// Policy — the pluggable decision function. MUST be pure and deterministic:
// no I/O, no wall-clock, no blocking, no unseeded randomness. Same (State, now)
// => same []Decision, every time. This is what makes replay reproducible and
// what lets one policy run unchanged in both backends.
type Policy interface {
    Schedule(s State, now Time) []Decision
    Name() string
}

// Executor — the backend seam. Dispatch is NON-BLOCKING. Completion is reported
// later, as a JobCompleted/JobFailed Event on the scheduler's event stream.
type Executor interface {
    Dispatch(d Decision) error   // start the job on the worker; returns immediately
    Cancel(id JobID) error       // propagate cancellation to a running job
}
```

### Events

```go
type Event interface { At() Time }

// concrete events (each carries At() and the relevant IDs):
//   JobSubmitted     — new job enters the system
//   JobCompleted     — a dispatched job finished (from either backend)
//   JobFailed        — job errored (nonzero exit, infra error)
//   WorkerRegistered — worker joined the pool
//   WorkerLost       — worker died / heartbeat missed
//   LeaseExpired     — a running job's lease elapsed (triggers reschedule)
//   CancelRequested  — external cancel for a job
//   Tick             — optional periodic wake (autoscaling/observability later)
```

### The scheduler core

```go
// Scheduler owns the pending queue, worker registry, and live assignments.
// It is single-threaded: exactly one goroutine calls Handle. Not thread-safe
// by design — concurrency lives in the driver/backend, never in scheduling logic.
type Scheduler struct {
    policy   Policy
    clock    Clock
    executor Executor
    // queues, worker registry, leases, decision log...
}

// Handle consumes one event, and if it changed schedulability, builds a State
// snapshot, calls policy.Schedule, enacts each Decision via executor.Dispatch,
// and records every Decision to the log. Returns the decisions for the driver
// (used by the sim driver to enqueue follow-on completion events).
func (s *Scheduler) Handle(e Event) []Decision
```

---

## The two drivers

There are **two drivers, one scheduler, one policy.** The driver owns time and the event source; the scheduler owns decisions.

### Real driver (wall-clock)

```go
for e := range eventCh {        // events pushed by Docker executor + external sources
    s.Handle(e)                 // clock = realClock{}; time.Now() under the hood
}
```

The Docker executor, on `Dispatch`, launches the container in a goroutine and, on exit, pushes `JobCompleted`/`JobFailed` onto `eventCh`. `LeaseExpired` is fired by a timer per assignment.

### Sim driver (logical time — classic discrete-event loop)

```go
for pq.Len() > 0 {              // pq = event priority queue, ordered by (At, seq)
    e := pq.Pop()               // next event in logical time
    simClock.set(e.At())        // advance logical time to it (no sleeping)
    decisions := s.Handle(e)
    // sim executor turned each Dispatch into a future JobCompleted at
    // now + modeledDuration(job, worker) and pushed it onto pq
}
```

`modeledDuration` encodes the workload model (class, size, and cache-affinity discount if the worker is warm). Tie-break equal timestamps by a monotonic sequence number so ordering is total and deterministic.

---

## Determinism constraints (must hold — they back the "deterministic replay" invariant)

- Policy is pure; no wall-clock, no I/O, no blocking inside it.
- No iteration over raw maps in scheduling logic — sort or use ordered structures.
- All randomness (workload generation, tie-breaking) comes from a **seeded** RNG passed in, never `rand` global state.
- Sim event queue breaks timestamp ties by insertion sequence number.
- Given the same workload file + seed + worker pool, two runs produce byte-identical decision logs.

---

## Invariant hooks

The scheduler is the natural place to enforce/check the invariants from the README:

- **capacity never exceeded** — reject/assert in `Handle` before enacting a Dispatch.
- **at-most-once** — a `JobCompleted` for an already-completed job is ignored; lease fencing rejects completions from expired leases.
- **dependencies respected** — a job only enters `Pending()` once all `Deps` have completed.
- **cancellation propagates** — `CancelRequested` removes from queue and calls `executor.Cancel` if running.
- **work-conservation (conditional)** — if a policy returns `Hold` while a runnable job and free capacity both exist, that is legal *only* if the policy declares it reserves capacity; otherwise it's an invariant violation. Log every `Hold` with its reason so this is auditable.

Run these as assertions in a `-race`-enabled test build and as a post-run check over the decision log.

---

## Minimal vertical slice (definition of "seam proven")

The first PR should prove the seam with the least code, not build features:

1. Domain types + the four interfaces (`Clock`, `State`, `Policy`, `Executor`) + `Scheduler.Handle`.
2. A trivial **FIFO policy** (dispatch the oldest pending job to any worker with free capacity; else `Hold`).
3. A **fake in-memory executor** for the real path (completes after a fixed fake delay via a timer) **and** a **sim executor** (schedules completion at `now + fixedDuration`).
4. Both drivers run the **same small job set** (e.g. 20 jobs, 3 workers).
5. **Acceptance:** the sim run's decision log is deterministic across repeated runs (identical seed → identical log), and the real run dispatches the same jobs to workers without ever exceeding capacity. The two need not match on timing, but must match on *which job went where* under FIFO.

If this slice holds, the interface serves both backends and everything else is additive. If it doesn't, the interface changes now — which is exactly why this is built first.

---

## Sub-decisions left to implementation (small, non-blocking)

- Package layout under `scheduler/`, `executor/`, `simulation/`, `api/` (interfaces likely in `api/`).
- Concrete event representation (interface + structs vs. tagged union).
- Error taxonomy for `Dispatch`/`Cancel`.
- Whether `State` is a materialized snapshot or a live read-only view (prefer snapshot for determinism).

## First task to hand Claude Code

> Implement the minimal vertical slice above: the domain types, the `Clock` / `State` / `Policy` / `Executor` interfaces and `Scheduler.Handle`, a FIFO policy, a fake real executor and a sim executor, and both drivers. Include table tests that assert the determinism and capacity invariants on a 20-job / 3-worker run. Do not add a second policy, Docker, or metrics yet.
