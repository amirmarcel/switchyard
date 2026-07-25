package executor

import (
	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
)

// RunReal drives the scheduler off a live event channel — the real,
// wall-clock backend. It blocks, handling one event at a time, until ch is
// closed.
func RunReal(s *scheduler.Scheduler, ch <-chan api.Event) {
	for e := range ch {
		s.Handle(e)
	}
}
