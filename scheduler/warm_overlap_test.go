// warm_overlap_test.go: proves enactDispatch computes and logs
// Decision.Factors["warm_overlap"] uniformly for every Dispatch — the
// input the simulator's warm-cache execution discount (ADR-0006) needs —
// regardless of which policy chose the placement. This lives with the
// scheduler package because the computation lives in enactDispatch, not in
// any Policy.
package scheduler_test

import (
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
	"github.com/amirmarcel/switchyard/simulation"
)

func runFIFO(jobs []api.Job, workers []api.Worker, duration api.Time) []api.Decision {
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

// TestWarmOverlapGradedByKeyMatch dispatches a job with two CacheKeys to a
// worker warm on only one of them and asserts warm_overlap comes out as the
// exact fraction (0.5), not just "some" or "none" — proving the discount
// input is graded by overlap, not a flat warm/cold signal. Uses FIFO (not
// PriorityAffinity) specifically to prove this is scheduler-core plumbing,
// not something only the affinity policy's own Factors provide.
func TestWarmOverlapGradedByKeyMatch(t *testing.T) {
	worker := api.Worker{ID: "w0", Capacity: api.Resources{CPUMillis: 1000}, WarmCache: []string{"key-a"}}
	job := api.Job{
		ID:        "job-0",
		Needs:     api.Resources{CPUMillis: 500},
		CacheKeys: []string{"key-a", "key-b"},
		SubmitAt:  0,
	}

	log := runFIFO([]api.Job{job}, []api.Worker{worker}, 1000)

	var found bool
	for _, d := range log {
		if d.Outcome == api.Dispatch && d.Job == job.ID {
			found = true
			if got, want := d.Factors["warm_overlap"], 0.5; got != want {
				t.Errorf("Factors[warm_overlap] = %v, want %v (1 of 2 keys warm)", got, want)
			}
		}
	}
	if !found {
		t.Fatal("job was never dispatched")
	}
}

// TestWarmOverlapAbsentWithoutCacheKeys asserts a job with no CacheKeys
// gets no warm_overlap factor at all — affinity, and so the discount, is
// undefined for such a job, matching PriorityAffinity's existing
// cache_affinity convention (priority_affinity.go).
func TestWarmOverlapAbsentWithoutCacheKeys(t *testing.T) {
	worker := api.Worker{ID: "w0", Capacity: api.Resources{CPUMillis: 1000}}
	job := api.Job{ID: "job-0", Needs: api.Resources{CPUMillis: 500}, SubmitAt: 0}

	log := runFIFO([]api.Job{job}, []api.Worker{worker}, 1000)

	var found bool
	for _, d := range log {
		if d.Outcome == api.Dispatch && d.Job == job.ID {
			found = true
			if _, ok := d.Factors["warm_overlap"]; ok {
				t.Errorf("Factors[warm_overlap] = %v, want absent for a job with no CacheKeys", d.Factors["warm_overlap"])
			}
		}
	}
	if !found {
		t.Fatal("job was never dispatched")
	}
}
