package simulation

import "github.com/amirmarcel/switchyard/api"

// Executor is the sim backend. Dispatch never runs anything — it computes
// the job's completion time (the workload's base duration, reduced by the
// warm-cache discount — see warmcache.go — when the dispatch landed on a
// worker already warm on some of the job's cache keys) and schedules a
// JobCompleted event at now+duration onto the shared queue.
type Executor struct {
	clock    *Clock
	queue    *EventQueue
	duration api.Time
}

func NewExecutor(clock *Clock, queue *EventQueue, duration api.Time) *Executor {
	return &Executor{clock: clock, queue: queue, duration: duration}
}

func (e *Executor) Dispatch(d api.Decision) error {
	duration := DiscountedDuration(e.duration, d.Factors["warm_overlap"])
	e.queue.Push(api.JobCompleted{
		Job:     d.Job,
		Worker:  d.Worker,
		LeaseID: d.LeaseID,
		Time:    e.clock.Now() + duration,
	})
	return nil
}

func (e *Executor) Cancel(id api.JobID) error { return nil }
