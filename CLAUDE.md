# CLAUDE.md — agent guidance for Switchyard

Switchyard is a **platform for evaluating CI scheduling policies** — one scheduler
core driving two backends (a real Docker executor on wall-clock time, and a
discrete-event simulator on logical time). The scheduler is the mechanism; the
benchmark harness is the product. Read `README.md` for the full framing and
`docs/design/scheduler-seam-spec.md` for the interface design. Those two documents
are the source of truth — this file points at them, it does not restate them.

## Non-negotiable invariants

The scheduler must never violate these. They are the correctness contract and
double as the test suite. Any change that could break one must add or update a
test proving it still holds.

- Every submitted job is **eventually scheduled** (no permanent starvation).
- No job **executes more than once** (at-most-once; lease fencing enforces this under failure).
- **Worker capacity is never exceeded.**
- **Job dependencies are respected** — a stage never starts before its predecessors complete.
- **Cancellation propagates** to running and queued work.
- **Retries preserve idempotency.**
- Scheduling is **deterministic** given the same workload and seed.
- **Work-conservation is conditional:** a policy may return `Hold` while a runnable
  job and free capacity exist *only* if it explicitly reserves capacity. Every
  `Hold` must be logged with its reason. An unexplained hold is a bug.

## Determinism constraints

These back the deterministic-replay invariant and are mandatory in any scheduling code:

- The `Policy` is **pure**: no I/O, no wall-clock (`time.Now`), no blocking, no global state.
- **No iteration over raw maps** in scheduling logic — sort or use ordered structures.
- All randomness comes from a **seeded RNG passed in**, never the `rand` global.
- The sim event queue **breaks timestamp ties by insertion sequence number**.
- Same workload file + seed + worker pool ⇒ byte-identical decision log across runs.

## Architecture rules

- The scheduler core is **single-threaded**. All concurrency lives in the drivers
  and executors, never in scheduling logic. Do not add locks to the scheduler to
  "make it concurrent" — that is a design violation, not a fix.
- The scheduler reads time only through the `Clock` interface.
- Dispatch is **non-blocking**; completion returns later as an event.
- Every scheduling action emits a first-class, logged `Decision` (dispatch or hold).

## Scope discipline

- **Do not sprawl.** Implement exactly what the current task asks. Do not add
  future subsystems (merge queue, autoscaler, remote cache, extra policies,
  Docker, metrics) unless the task names them.
- **Folders are created as they fill, not upfront.** No empty placeholder packages.
- If a task seems to require going beyond its stated scope, stop and flag it
  rather than expanding silently.

## Workflow

- Work on a feature branch, never commit directly to `main` (it is protected).
- Commits follow **conventional commit** format: `type: subject`
  (`feat:`, `fix:`, `test:`, `docs:`, `chore:`, `ci:`, `refactor:`).
- Tests run with `-race`. New scheduling logic ships with tests asserting the
  relevant invariants above.
- Design decisions of any weight get an ADR in `docs/adr/` (see `template.md`),
  co-written with the maintainer — the reasoning is the maintainer's, not the agent's.
- CI (`build-test-lint`) must pass before merge.

## Style

- Idiomatic Go; pass `golangci-lint`.
- Prefer small, reviewable changes. One concern per PR.
