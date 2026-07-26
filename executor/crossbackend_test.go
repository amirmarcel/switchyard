// crossbackend_test.go: the seam spec's acceptance bar
// (docs/design/scheduler-seam-spec.md — "must match on which job went where
// under FIFO") asserted directly, comparing the sim and real backends'
// decision logs on the same workload. See
// docs/reviews/2026-07-25-opus-code-review.md, finding G4.
package executor_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/executor"
	"github.com/amirmarcel/switchyard/scheduler"
	"github.com/amirmarcel/switchyard/simulation"
)

// TestSimAndRealAgreeOnPlacementUnderFIFO drives the same jobs and worker
// pool through both backends under FIFO and asserts they agree on which job
// went to which worker. Timing is expected to differ — the sim backend runs
// in instantaneous logical time, the real backend actually sleeps — only
// placement is compared, matching the seam spec's own acceptance criterion.
func TestSimAndRealAgreeOnPlacementUnderFIFO(t *testing.T) {
	// Every job's Needs equal a whole worker's Capacity and numJobs ==
	// numWorkers, so each job dispatches immediately on submission — no job
	// ever waits for a worker to free up. That matters here: once a job has
	// to wait for a completion, which worker frees first is a real-time
	// race (goroutine scheduling, not scheduling logic), so placement would
	// legitimately diverge between a wall-clock backend and an
	// instantaneous logical-time one without either side having a bug. The
	// seam spec's acceptance bar is about the scheduler's own placement
	// logic agreeing across backends, not about reproducing OS scheduling
	// jitter.
	const numJobs = 3
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

	simLog := runSimFIFO(jobs, workers, 1000)
	realLog := runRealFIFO(t, jobs, workers)

	simPlacement := jobPlacement(simLog)
	realPlacement := jobPlacement(realLog)

	if len(simPlacement) != numJobs {
		t.Fatalf("sim backend dispatched %d jobs, want %d", len(simPlacement), numJobs)
	}
	if len(realPlacement) != numJobs {
		t.Fatalf("real backend dispatched %d jobs, want %d", len(realPlacement), numJobs)
	}
	if !reflect.DeepEqual(simPlacement, realPlacement) {
		t.Fatalf("sim and real backends disagree on job->worker placement under FIFO:\nsim:  %+v\nreal: %+v", simPlacement, realPlacement)
	}
}

// jobPlacement reduces a decision log to its job->worker mapping, ignoring
// everything else (timing, Holds, ordering) — the one thing the seam spec
// requires the two backends to agree on.
func jobPlacement(log []api.Decision) map[api.JobID]api.WorkerID {
	placement := make(map[api.JobID]api.WorkerID)
	for _, d := range log {
		if d.Outcome == api.Dispatch {
			placement[d.Job] = d.Worker
		}
	}
	return placement
}

func runSimFIFO(jobs []api.Job, workers []api.Worker, duration api.Time) []api.Decision {
	clock := &simulation.Clock{}
	queue := simulation.NewEventQueue()
	exec := simulation.NewExecutor(clock, queue, duration)
	sched := scheduler.NewScheduler(scheduler.FIFO{}, clock, exec)

	for _, w := range workers {
		queue.Push(api.WorkerRegistered{Worker: w, Time: 0})
	}
	for _, j := range jobs {
		queue.Push(api.JobSubmitted{Job: j, Time: j.SubmitAt})
	}

	simulation.Run(sched, clock, queue)
	return sched.DecisionLog()
}

// runRealFIFO mirrors TestRealDriverCapacityInvariant's setup: it drives the
// same jobs/workers through the real (wall-clock, goroutine-based) backend
// under FIFO and returns the resulting decision log once every job has
// completed.
func runRealFIFO(t *testing.T, jobs []api.Job, workers []api.Worker) []api.Decision {
	t.Helper()

	clock := executor.RealClock{}
	ch := make(chan api.Event, len(jobs)+len(workers))
	fake := executor.NewFakeExecutor(ch, 5*time.Millisecond, clock)
	sched := scheduler.NewScheduler(scheduler.FIFO{}, clock, fake)

	for _, w := range workers {
		ch <- api.WorkerRegistered{Worker: w, Time: clock.Now()}
	}
	for _, j := range jobs {
		ch <- api.JobSubmitted{Job: j, Time: clock.Now()}
	}

	tap := make(chan api.Event, len(jobs)+len(workers))
	go func() {
		completed := 0
		for e := range ch {
			tap <- e
			if _, ok := e.(api.JobCompleted); ok {
				completed++
				if completed == len(jobs) {
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

	return sched.DecisionLog()
}
