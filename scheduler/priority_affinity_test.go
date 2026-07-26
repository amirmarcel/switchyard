// priority_affinity_test.go: proves the three properties
// docs/design/candidate-policy-spec.md requires of PriorityAffinity beyond
// what fifo.go's tests already cover for the shared scheduler machinery —
// determinism, bounded no-starvation under priority ordering, and warm-worker
// placement. The work-conservation requirement is proved separately in
// bench/invariants_test.go, since checkWorkConservation lives in bench.
package scheduler_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
	"github.com/amirmarcel/switchyard/simulation"
)

// runPriorityAffinity wires a fresh Scheduler + sim backend under
// PriorityAffinity, seeds it with worker registrations and job submissions,
// drains it to completion, and returns the resulting decision log.
func runPriorityAffinity(jobs []api.Job, workers []api.Worker, duration api.Time) []api.Decision {
	clock := &simulation.Clock{}
	queue := simulation.NewEventQueue()
	exec := simulation.NewExecutor(clock, queue, duration)
	sched := scheduler.NewScheduler(scheduler.PriorityAffinity{}, clock, exec)

	for _, w := range workers {
		queue.Push(api.WorkerRegistered{Worker: w, Time: 0})
	}
	for _, j := range jobs {
		queue.Push(api.JobSubmitted{Job: j, Time: j.SubmitAt})
	}

	simulation.Run(sched, clock, queue)
	return sched.DecisionLog()
}

// TestPriorityAffinityDeterminism asserts the determinism invariant under
// the new policy: same workload + worker pool + seed (priority+affinity has
// no randomness of its own, so this is about the sim's replay) => a
// byte-identical decision log across reruns, mixing priorities and
// overlapping cache keys so both mechanisms are exercised.
func TestPriorityAffinityDeterminism(t *testing.T) {
	var jobs []api.Job
	for i := 0; i < 30; i++ {
		jobs = append(jobs, api.Job{
			ID:        api.JobID(fmt.Sprintf("job-%02d", i)),
			Class:     api.Batch,
			Needs:     api.Resources{CPUMillis: 500},
			Priority:  i % 3,
			CacheKeys: []string{fmt.Sprintf("key-%d", i%4)},
			SubmitAt:  api.Time(i % 5),
		})
	}
	workers := make([]api.Worker, 4)
	for i := range workers {
		workers[i] = api.Worker{ID: api.WorkerID(fmt.Sprintf("worker-%d", i)), Capacity: api.Resources{CPUMillis: 1000}}
	}

	log1 := runPriorityAffinity(jobs, workers, 1000)
	log2 := runPriorityAffinity(jobs, workers, 1000)

	if len(log1) == 0 {
		t.Fatal("expected at least one decision")
	}
	if !reflect.DeepEqual(log1, log2) {
		t.Fatalf("decision logs diverged across reruns with identical input:\n%+v\n%+v", log1, log2)
	}
}

// TestPriorityAffinityNoStarvationUnderBoundedHighPriorityLoad is the test
// docs/design/candidate-policy-spec.md asks for explicitly: pure priority
// ordering is accepted for v1 (no aging), but a *bounded* set of
// high-priority jobs must not permanently starve low-priority ones — once
// the high-priority jobs drain, the low-priority jobs must still dispatch.
// Sized so every job needs a whole worker (one dispatch per completion),
// forcing the low-priority jobs to wait behind every high-priority one.
func TestPriorityAffinityNoStarvationUnderBoundedHighPriorityLoad(t *testing.T) {
	const numWorkers = 2
	const numHigh = 6
	const numLow = 4

	var jobs []api.Job
	for i := 0; i < numHigh; i++ {
		jobs = append(jobs, api.Job{
			ID:       api.JobID(fmt.Sprintf("high-%02d", i)),
			Needs:    api.Resources{CPUMillis: 1000},
			Priority: 10,
			SubmitAt: 0,
		})
	}
	for i := 0; i < numLow; i++ {
		jobs = append(jobs, api.Job{
			ID:       api.JobID(fmt.Sprintf("low-%02d", i)),
			Needs:    api.Resources{CPUMillis: 1000},
			Priority: 0,
			SubmitAt: 0,
		})
	}
	workers := make([]api.Worker, numWorkers)
	for i := range workers {
		workers[i] = api.Worker{ID: api.WorkerID(fmt.Sprintf("worker-%d", i)), Capacity: api.Resources{CPUMillis: 1000}}
	}

	log := runPriorityAffinity(jobs, workers, 1000)

	dispatchCount := make(map[api.JobID]int)
	dispatchAt := make(map[api.JobID]api.Time)
	for _, d := range log {
		if d.Outcome == api.Dispatch {
			dispatchCount[d.Job]++
			dispatchAt[d.Job] = d.At
		}
	}

	var lastHigh, firstLow api.Time = -1, -1
	for _, j := range jobs {
		if got := dispatchCount[j.ID]; got != 1 {
			t.Errorf("job %s dispatched %d times, want exactly 1", j.ID, got)
		}
		at := dispatchAt[j.ID]
		if j.Priority == 10 && at > lastHigh {
			lastHigh = at
		}
		if j.Priority == 0 && (firstLow == -1 || at < firstLow) {
			firstLow = at
		}
	}
	if firstLow < lastHigh {
		t.Errorf("a low-priority job dispatched at t=%d before the last high-priority job at t=%d — priority ordering not honored", firstLow, lastHigh)
	}
}

// TestPriorityAffinityPlacesOnWarmWorker is the affinity placement test:
// given one worker already warm on the job's cache keys and one cold worker
// with identical free capacity, the policy must place the job on the warm
// one — not whichever comes first in worker order (the cold worker is
// registered first, so a naive first-fit would pick it).
func TestPriorityAffinityPlacesOnWarmWorker(t *testing.T) {
	cold := api.Worker{ID: "cold", Capacity: api.Resources{CPUMillis: 1000}}
	warm := api.Worker{ID: "warm", Capacity: api.Resources{CPUMillis: 1000}, WarmCache: []string{"repo-a"}}
	job := api.Job{ID: "job-0", Needs: api.Resources{CPUMillis: 500}, CacheKeys: []string{"repo-a"}, SubmitAt: 0}

	log := runPriorityAffinity([]api.Job{job}, []api.Worker{cold, warm}, 1000)

	var dispatched bool
	for _, d := range log {
		if d.Outcome == api.Dispatch && d.Job == job.ID {
			dispatched = true
			if d.Worker != warm.ID {
				t.Errorf("job dispatched to worker %s, want the warm worker %s", d.Worker, warm.ID)
			}
			if d.Factors["warm_keys_matched"] != 1 {
				t.Errorf("Factors[warm_keys_matched] = %v, want 1", d.Factors["warm_keys_matched"])
			}
		}
	}
	if !dispatched {
		t.Fatal("job was never dispatched")
	}
}
