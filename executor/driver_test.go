package executor_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/executor"
	"github.com/amirmarcel/switchyard/scheduler"
)

// TestRealDriverCapacityInvariant proves the same FIFO policy, run against
// the real (wall-clock, goroutine-based) backend instead of the sim
// backend, dispatches every job exactly once without ever exceeding
// worker capacity — the acceptance bar for the real path from the seam
// spec. Timing need not (and does not) match the sim run; only which jobs
// got scheduled does.
func TestRealDriverCapacityInvariant(t *testing.T) {
	const numJobs = 20
	const numWorkers = 3
	const cpu = 1000

	jobs := make([]api.Job, numJobs)
	for i := range jobs {
		jobs[i] = api.Job{ID: api.JobID(fmt.Sprintf("job-%02d", i)), Needs: api.Resources{CPUMillis: cpu}}
	}
	workers := make([]api.Worker, numWorkers)
	for i := range workers {
		workers[i] = api.Worker{ID: api.WorkerID(fmt.Sprintf("worker-%d", i)), Capacity: api.Resources{CPUMillis: cpu}}
	}

	clock := executor.RealClock{}
	ch := make(chan api.Event, numJobs+numWorkers)
	fake := executor.NewFakeExecutor(ch, 5*time.Millisecond, clock)
	sched := scheduler.NewScheduler(scheduler.FIFO{}, clock, fake)

	for _, w := range workers {
		ch <- api.WorkerRegistered{Worker: w, Time: clock.Now()}
	}
	for _, j := range jobs {
		ch <- api.JobSubmitted{Job: j, Time: clock.Now()}
	}

	// tap forwards events to RunReal, which is the sole goroutine allowed
	// to touch the scheduler (Handle is not safe for concurrent use by
	// design). The forwarding goroutine itself never touches the
	// scheduler — it only counts completions and closes tap once every
	// job is done, which stops RunReal and lets the main goroutine safely
	// inspect the scheduler afterward.
	tap := make(chan api.Event, numJobs+numWorkers)
	go func() {
		completed := 0
		for e := range ch {
			tap <- e
			if _, ok := e.(api.JobCompleted); ok {
				completed++
				if completed == numJobs {
					close(tap)
					return
				}
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		executor.RunReal(sched, tap)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all jobs to complete on the real driver")
	}

	dispatchCount := make(map[api.JobID]int, numJobs)
	for _, d := range sched.DecisionLog() {
		if d.Outcome == api.Dispatch {
			dispatchCount[d.Job]++
		}
	}
	for _, j := range jobs {
		if got := dispatchCount[j.ID]; got != 1 {
			t.Errorf("job %s dispatched %d times, want exactly 1", j.ID, got)
		}
	}
}
