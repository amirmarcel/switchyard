package simulation

import "github.com/amirmarcel/switchyard/api"

// Executor is the sim backend. Dispatch never runs anything — it computes
// the job's completion time (a fixed duration, for this slice) and
// schedules a JobCompleted event at now+duration onto the shared queue.
type Executor struct {
	clock    *Clock
	queue    *EventQueue
	duration api.Time
}

func NewExecutor(clock *Clock, queue *EventQueue, duration api.Time) *Executor {
	return &Executor{clock: clock, queue: queue, duration: duration}
}

func (e *Executor) Dispatch(d api.Decision) error {
	e.queue.Push(api.JobCompleted{
		Job:    d.Job,
		Worker: d.Worker,
		Time:   e.clock.Now() + e.duration,
	})
	return nil
}

func (e *Executor) Cancel(id api.JobID) error { return nil }
