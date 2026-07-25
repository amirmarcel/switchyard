package simulation

import "github.com/amirmarcel/switchyard/scheduler"

// Run drives the scheduler over logical time: pop the next event in
// (timestamp, sequence) order, advance the clock to it — never sleep —
// and hand it to the scheduler. The sim executor pushes JobCompleted
// events back onto queue, so the loop drains until none remain.
func Run(s *scheduler.Scheduler, clock *Clock, queue *EventQueue) {
	for {
		e, ok := queue.Pop()
		if !ok {
			return
		}
		clock.set(e.At())
		s.Handle(e)
	}
}
