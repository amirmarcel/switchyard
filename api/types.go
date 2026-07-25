// Package api defines the domain types and the seam interfaces (Clock,
// State, Policy, Executor) that the scheduler core and both backends share.
// This is the vertical-slice implementation of the seam described in
// docs/design/scheduler-seam-spec.md: keeping it dependency-free is what
// lets the real and sim backends (packages executor and simulation) each
// depend on it without depending on each other.
package api

// JobID, WorkerID, and Time are distinct types so they can't be confused
// with each other or with plain strings/ints at call sites.
type JobID string
type WorkerID string

// Time is nanoseconds; the real clock reads wall time, the sim clock
// advances logical time.
type Time int64

type WorkloadClass int

const (
	Interactive WorkloadClass = iota
	Batch
	Agent
)

type Resources struct {
	CPUMillis int
	MemBytes  int64
}

type Job struct {
	ID        JobID
	Class     WorkloadClass
	Needs     Resources
	Priority  int // higher = more urgent
	Deps      []JobID
	CacheKeys []string
	SubmitAt  Time
	Timeout   Time // deadline relative to dispatch; 0 = none
}

type Worker struct {
	ID        WorkerID
	Capacity  Resources
	WarmCache []string
}

type Assignment struct {
	Job     JobID
	Worker  WorkerID
	LeaseID string
	StartAt Time
}

type Outcome int

const (
	Dispatch Outcome = iota
	Hold
	Reject // admission-time: job can never fit any worker, never enters Pending
)

// Decision is the first-class, logged unit of every scheduling action,
// whether it dispatches or deliberately holds.
type Decision struct {
	Outcome    Outcome
	Job        JobID
	Worker     WorkerID // set when Outcome == Dispatch
	Policy     string
	Factors    map[string]float64
	QueueDelay Time
	At         Time
}
