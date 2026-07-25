package executor

import (
	"time"

	"github.com/amirmarcel/switchyard/api"
)

// FakeExecutor is an in-memory stand-in for the real Docker executor. It
// matches the same non-blocking contract: Dispatch returns immediately,
// and completion is reported later — after a fixed delay, from a
// goroutine — as a JobCompleted event on Events.
type FakeExecutor struct {
	Events chan<- api.Event
	Delay  time.Duration
	clock  api.Clock
}

func NewFakeExecutor(events chan<- api.Event, delay time.Duration, clock api.Clock) *FakeExecutor {
	return &FakeExecutor{Events: events, Delay: delay, clock: clock}
}

func (e *FakeExecutor) Dispatch(d api.Decision) error {
	go func() {
		time.Sleep(e.Delay)
		e.Events <- api.JobCompleted{Job: d.Job, Worker: d.Worker, Time: e.clock.Now()}
	}()
	return nil
}

func (e *FakeExecutor) Cancel(id api.JobID) error { return nil }
