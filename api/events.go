package api

// Event is the unified stream the scheduler consumes. Only the events this
// slice needs are defined here; more are added as later tasks need them.
type Event interface{ At() Time }

type JobSubmitted struct {
	Job  Job
	Time Time
}

func (e JobSubmitted) At() Time { return e.Time }

type JobCompleted struct {
	Job    JobID
	Worker WorkerID
	// LeaseID is the lease the dispatch that produced this completion was
	// issued under. The scheduler rejects a completion whose LeaseID
	// doesn't match the assignment's current lease — see
	// docs/adr/0005-lease-fencing.md.
	LeaseID string
	Time    Time
}

func (e JobCompleted) At() Time { return e.Time }

type JobFailed struct {
	Job     JobID
	Worker  WorkerID
	LeaseID string
	Err     error
	Time    Time
}

func (e JobFailed) At() Time { return e.Time }

type WorkerRegistered struct {
	Worker Worker
	Time   Time
}

func (e WorkerRegistered) At() Time { return e.Time }

type CancelRequested struct {
	Job  JobID
	Time Time
}

func (e CancelRequested) At() Time { return e.Time }
