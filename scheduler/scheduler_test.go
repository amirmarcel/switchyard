// scheduler_test.go: sim-backed FIFO harness proving the determinism and
// capacity invariants on a 20-job/3-worker run (see executor/driver_test.go
// for the same acceptance bar on the real backend).
package scheduler_test

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
	"github.com/amirmarcel/switchyard/simulation"
)

type scenario struct {
	name     string
	jobs     []api.Job
	workers  []api.Worker
	duration api.Time
}

func uniformJobs(n int, cpu int) []api.Job {
	jobs := make([]api.Job, n)
	for i := 0; i < n; i++ {
		jobs[i] = api.Job{
			ID:       api.JobID(fmt.Sprintf("job-%02d", i)),
			Class:    api.Batch,
			Needs:    api.Resources{CPUMillis: cpu},
			SubmitAt: 0,
		}
	}
	return jobs
}

func uniformWorkers(n int, cpu int) []api.Worker {
	workers := make([]api.Worker, n)
	for i := 0; i < n; i++ {
		workers[i] = api.Worker{
			ID:       api.WorkerID(fmt.Sprintf("worker-%d", i)),
			Capacity: api.Resources{CPUMillis: cpu},
		}
	}
	return workers
}

func scenarios() []scenario {
	return []scenario{
		{name: "20 jobs 3 workers", jobs: uniformJobs(20, 1000), workers: uniformWorkers(3, 1000), duration: 1000},
		{name: "5 jobs 2 workers", jobs: uniformJobs(5, 1000), workers: uniformWorkers(2, 1000), duration: 500},
	}
}

// runSim wires a fresh Scheduler + sim backend, seeds it with worker
// registrations and job submissions, drains it to completion, and returns
// the resulting decision log.
func runSim(sc scenario) []api.Decision {
	clock := &simulation.Clock{}
	queue := simulation.NewEventQueue()
	exec := simulation.NewExecutor(clock, queue, sc.duration)
	sched := scheduler.NewScheduler(scheduler.FIFO{}, clock, exec)

	for _, w := range sc.workers {
		queue.Push(api.WorkerRegistered{Worker: w, Time: 0})
	}
	for _, j := range sc.jobs {
		queue.Push(api.JobSubmitted{Job: j, Time: j.SubmitAt})
	}

	simulation.Run(sched, clock, queue)
	return sched.DecisionLog()
}

// TestDeterminism asserts the determinism invariant: given the same
// workload, worker pool, and (fixed, since FIFO has no randomness) seed,
// the sim's decision log is byte-identical across reruns.
func TestDeterminism(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			log1 := runSim(sc)
			log2 := runSim(sc)

			if len(log1) == 0 {
				t.Fatal("expected at least one decision")
			}
			if !reflect.DeepEqual(log1, log2) {
				t.Fatalf("decision logs diverged across reruns with identical input:\n%+v\n%+v", log1, log2)
			}
		})
	}
}

// TestCapacityInvariant asserts worker capacity is never exceeded and that
// every job is dispatched exactly once (eventually scheduled, at-most-once),
// reconstructed purely from the decision log.
func TestCapacityInvariant(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			log := runSim(sc)

			jobNeeds := make(map[api.JobID]api.Resources, len(sc.jobs))
			for _, j := range sc.jobs {
				jobNeeds[j.ID] = j.Needs
			}
			workerCap := make(map[api.WorkerID]api.Resources, len(sc.workers))
			for _, w := range sc.workers {
				workerCap[w.ID] = w.Capacity
			}

			assertNoCapacityViolation(t, log, jobNeeds, workerCap, sc.duration)

			dispatchCount := make(map[api.JobID]int, len(sc.jobs))
			for _, d := range log {
				if d.Outcome == api.Dispatch {
					dispatchCount[d.Job]++
				}
			}
			for _, j := range sc.jobs {
				if got := dispatchCount[j.ID]; got != 1 {
					t.Errorf("job %s dispatched %d times, want exactly 1", j.ID, got)
				}
			}
		})
	}
}

// TestAdmissionRejectsUnplaceableJob exercises non-uniform job/worker
// sizing: a job whose Needs exceed every worker's capacity must be
// rejected at submission, never entered into Pending, and must not wedge
// jobs that fit immediately behind it in the queue. See
// docs/adr/0001-admission-check-for-unplaceable-jobs.md.
func TestAdmissionRejectsUnplaceableJob(t *testing.T) {
	oversized := api.Job{ID: "oversized", Needs: api.Resources{CPUMillis: 5000}}
	var normals []api.Job
	for i := 0; i < 5; i++ {
		normals = append(normals, api.Job{ID: api.JobID(fmt.Sprintf("normal-%d", i)), Needs: api.Resources{CPUMillis: 1000}})
	}
	sc := scenario{
		name:     "oversized job at head, normals behind it",
		jobs:     append([]api.Job{oversized}, normals...),
		workers:  uniformWorkers(3, 1000),
		duration: 1000,
	}

	log := runSim(sc)

	dispatchCount := make(map[api.JobID]int)
	rejectCount := make(map[api.JobID]int)
	for _, d := range log {
		switch d.Outcome {
		case api.Dispatch:
			dispatchCount[d.Job]++
		case api.Reject:
			rejectCount[d.Job]++
		}
	}

	if rejectCount[oversized.ID] != 1 {
		t.Errorf("oversized job rejected %d times, want exactly 1", rejectCount[oversized.ID])
	}
	if dispatchCount[oversized.ID] != 0 {
		t.Errorf("oversized job dispatched %d times, want 0 — it can never fit any worker", dispatchCount[oversized.ID])
	}
	for _, j := range normals {
		if got := dispatchCount[j.ID]; got != 1 {
			t.Errorf("job %s dispatched %d times, want exactly 1 — an unplaceable head job must not wedge jobs that fit", j.ID, got)
		}
	}
}

// TestNoStarvationWithNonUniformSizing proves the no-starvation invariant
// holds once unplaceable jobs are rejected at admission: a job that is
// only *temporarily* unplaceable (it fits some worker, just not one that's
// free yet) still gets scheduled once capacity frees — even though an
// always-unplaceable job was submitted ahead of it in the same queue.
func TestNoStarvationWithNonUniformSizing(t *testing.T) {
	oversized := api.Job{ID: "oversized", Needs: api.Resources{CPUMillis: 5000}}
	filler := api.Job{ID: "filler", Needs: api.Resources{CPUMillis: 1000}}
	head := api.Job{ID: "head", Needs: api.Resources{CPUMillis: 1000}}

	sc := scenario{
		name: "oversized ahead of a temporarily-blocked-but-fits job",
		jobs: []api.Job{oversized, filler, head},
		workers: []api.Worker{
			{ID: "worker-small", Capacity: api.Resources{CPUMillis: 500}},
			{ID: "worker-big", Capacity: api.Resources{CPUMillis: 1000}},
		},
		duration: 1000,
	}

	log := runSim(sc)

	dispatches := make(map[api.JobID][]api.Time)
	rejectCount := make(map[api.JobID]int)
	for _, d := range log {
		switch d.Outcome {
		case api.Dispatch:
			dispatches[d.Job] = append(dispatches[d.Job], d.At)
		case api.Reject:
			rejectCount[d.Job]++
		}
	}

	if rejectCount[oversized.ID] != 1 || len(dispatches[oversized.ID]) != 0 {
		t.Errorf("oversized job: want 1 reject and 0 dispatches, got %d rejects and %d dispatches", rejectCount[oversized.ID], len(dispatches[oversized.ID]))
	}
	if got := dispatches[filler.ID]; len(got) != 1 || got[0] != 0 {
		t.Errorf("filler: want exactly 1 dispatch at t=0 (only worker-big fits it), got %v", got)
	}
	if got := dispatches[head.ID]; len(got) != 1 || got[0] != sc.duration {
		t.Errorf("head: want exactly 1 dispatch at t=%d, once worker-big frees from filler — got %v (permanent starvation if empty)", sc.duration, got)
	}
}

type interval struct {
	start, end api.Time
	needs      api.Resources
}

func assertNoCapacityViolation(t *testing.T, log []api.Decision, jobNeeds map[api.JobID]api.Resources, workerCap map[api.WorkerID]api.Resources, duration api.Time) {
	t.Helper()

	byWorker := make(map[api.WorkerID][]interval)
	for _, d := range log {
		if d.Outcome != api.Dispatch {
			continue
		}
		byWorker[d.Worker] = append(byWorker[d.Worker], interval{
			start: d.At,
			end:   d.At + duration,
			needs: jobNeeds[d.Job],
		})
	}

	workerIDs := make([]api.WorkerID, 0, len(byWorker))
	for w := range byWorker {
		workerIDs = append(workerIDs, w)
	}
	sort.Slice(workerIDs, func(i, j int) bool { return workerIDs[i] < workerIDs[j] })

	for _, w := range workerIDs {
		intervals := byWorker[w]
		cap := workerCap[w]
		for i := range intervals {
			var used api.Resources
			for j := range intervals {
				if intervals[j].start < intervals[i].end && intervals[i].start < intervals[j].end {
					used.CPUMillis += intervals[j].needs.CPUMillis
					used.MemBytes += intervals[j].needs.MemBytes
				}
			}
			if used.CPUMillis > cap.CPUMillis || used.MemBytes > cap.MemBytes {
				t.Fatalf("capacity exceeded on worker %s at t=%d: used %+v > capacity %+v", w, intervals[i].start, used, cap)
			}
		}
	}
}
