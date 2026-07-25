package api

// Clock — the scheduler and policy read time only from here.
type Clock interface{ Now() Time }

// State is a read-only snapshot the policy reasons over. Ordering must be
// stable (deterministic) — implementations must never expose raw map
// iteration order.
type State interface {
	Pending() []Job
	Workers() []Worker
	Running() []Assignment
	FreeCapacity(WorkerID) Resources
}

// Policy is the pluggable decision function. It MUST be pure and
// deterministic: no I/O, no wall-clock, no blocking, no unseeded
// randomness. Same (State, now) => same []Decision, every time.
type Policy interface {
	Schedule(s State, now Time) []Decision
	Name() string
}

// Executor is the backend seam. Dispatch is non-blocking; completion is
// reported later, as a JobCompleted/JobFailed Event on the scheduler's
// event stream.
type Executor interface {
	Dispatch(d Decision) error
	Cancel(id JobID) error
}
