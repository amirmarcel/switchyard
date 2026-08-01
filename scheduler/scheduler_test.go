// scheduler_test.go: sim-backed FIFO harness proving the determinism and
// capacity invariants on a 20-job/3-worker run (see executor/driver_test.go
// for the same acceptance bar on the real backend).
package scheduler_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/bench"
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
		// Two 400-CPU jobs against one 1000-CPU worker: both fit
		// concurrently on the same worker (bin-packing), unlike every other
		// scenario here where each job saturates a whole worker on its own.
		// Exercises enactDispatch's partial-capacity arithmetic and FIFO's
		// running-free decrement directly at the scheduler level, not only
		// through bench (see docs/reviews/2026-07-25-opus-code-review.md, G1).
		{name: "bin-packing 2 jobs 1 worker", jobs: uniformJobs(2, 400), workers: uniformWorkers(1, 1000), duration: 1000},
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

			w := bench.Workload{
				Workers:     sc.workers,
				JobDuration: sc.duration,
			}
			for _, j := range sc.jobs {
				w.Jobs = append(w.Jobs, bench.TimedJob{Job: j, SubmitAt: j.SubmitAt})
			}
			if violations := bench.CheckCapacityInvariant(w, log); len(violations) != 0 {
				t.Fatalf("capacity invariant violated: %v", violations)
			}

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

// TestLeaseFencingRejectsStaleCompletion is the review's exact reproduction
// for F1: a job dispatched under lease L1 fails and is retried, minting a
// new lease L2 for the same worker; the *original* run's completion then
// arrives late, after the retry is already live. Worker-ID matching alone
// cannot catch this — the stale completion carries the correct (unchanged)
// worker ID, since the retry landed on the same worker. Only the
// per-dispatch lease token distinguishes "belongs to the assignment I
// currently hold" from "belongs to a superseded assignment." See
// docs/adr/0005-lease-fencing.md.
//
// Before lease fencing, this event log would be accepted as a real
// completion by the old (a.Worker != ev.Worker)-only check — wrongly
// marking the job Completed at t=20 (the stale run) while silently
// dropping the genuine completion at t=30, because the wrongly-accepted
// stale event had already deleted the running assignment. After the fix,
// t=20 is fenced and t=30 is the one and only accepted completion.
func TestLeaseFencingRejectsStaleCompletion(t *testing.T) {
	job := api.Job{ID: "j", Needs: api.Resources{CPUMillis: 1000}}
	worker := api.Worker{ID: "w0", Capacity: api.Resources{CPUMillis: 1000}}

	clock := &simulation.Clock{}
	queue := simulation.NewEventQueue()
	exec := simulation.NewExecutor(clock, queue, 1000)
	sched := scheduler.NewScheduler(scheduler.FIFO{}, clock, exec)

	queue.Push(api.WorkerRegistered{Worker: worker, Time: 0})
	queue.Push(api.JobSubmitted{Job: job, Time: 0})
	// run#1 dispatches at t=0 under lease "j-1" (deterministic: jobID +
	// scheduler-wide monotonic counter, first dispatch overall).
	// run#1 fails at t=10, accepted (matches the current lease "j-1"):
	// the assignment is released and FIFO immediately retries, minting
	// lease "j-2" for the same job on the same worker.
	queue.Push(api.JobFailed{Job: job.ID, Worker: worker.ID, LeaseID: "j-1", Time: 10})
	// run#1's original completion lands late, after run#2 is already live
	// under "j-2" — this must be fenced, not accepted.
	queue.Push(api.JobCompleted{Job: job.ID, Worker: worker.ID, LeaseID: "j-1", Time: 20})
	// run#2's genuine completion, under the current lease.
	queue.Push(api.JobCompleted{Job: job.ID, Worker: worker.ID, LeaseID: "j-2", Time: 30})

	simulation.Run(sched, clock, queue)
	log := sched.DecisionLog()

	var fenced, completed []api.Decision
	for _, d := range log {
		switch d.Outcome {
		case api.Fenced:
			fenced = append(fenced, d)
		case api.Completed:
			completed = append(completed, d)
		}
	}

	if len(fenced) != 1 {
		t.Fatalf("want exactly 1 Fenced decision (the stale run#1 completion), got %d: %+v", len(fenced), fenced)
	}
	if fenced[0].At != 20 || fenced[0].LeaseID != "j-1" {
		t.Errorf("fenced decision = %+v, want At=20 LeaseID=j-1 (the stale completion)", fenced[0])
	}

	if len(completed) != 1 {
		t.Fatalf("want exactly 1 Completed decision (the genuine run#2 completion), got %d: %+v", len(completed), completed)
	}
	if completed[0].At != 30 || completed[0].LeaseID != "j-2" {
		t.Errorf("completed decision = %+v, want At=30 LeaseID=j-2 (the genuine completion) — the stale completion must not be counted", completed[0])
	}

	dispatchCount := 0
	for _, d := range log {
		if d.Outcome == api.Dispatch {
			dispatchCount++
		}
	}
	if dispatchCount != 2 {
		t.Errorf("want exactly 2 dispatches (initial + retry after JobFailed), got %d", dispatchCount)
	}
}

// TestNormalCompletionAcceptedUnderMatchingLease is the non-regression
// counterpart to TestLeaseFencingRejectsStaleCompletion: a completion whose
// lease matches the assignment's current lease (the ordinary, no-failure
// case) must still be accepted exactly once.
func TestNormalCompletionAcceptedUnderMatchingLease(t *testing.T) {
	for _, sc := range scenarios() {
		t.Run(sc.name, func(t *testing.T) {
			log := runSim(sc)

			completedCount := make(map[api.JobID]int, len(sc.jobs))
			for _, d := range log {
				if d.Outcome == api.Completed {
					completedCount[d.Job]++
				}
				if d.Outcome == api.Fenced {
					t.Errorf("unexpected Fenced decision in a normal run with no reassignment: %+v", d)
				}
			}
			for _, j := range sc.jobs {
				if got := completedCount[j.ID]; got != 1 {
					t.Errorf("job %s completed %d times, want exactly 1", j.ID, got)
				}
			}
		})
	}
}

// TestWorkerRegisteredRejectsNowUnplaceablePending is the review's exact
// reproduction (finding F2): a job submitted before any worker exists is
// provisionally admitted (admissible can't evaluate it yet); a worker then
// registers too small for it. Before this fix, admission was never
// revisited, so the oversized job Held forever and every normal job behind
// it in FIFO's strict head-of-line queue starved permanently — the same
// bug ADR-0001 already fixed for the submitted-after-workers-exist case,
// resurrected by event ordering. This must fail before the fix (normals
// never dispatch) and pass after (oversized rejected, normals dispatch).
func TestWorkerRegisteredRejectsNowUnplaceablePending(t *testing.T) {
	oversized := api.Job{ID: "oversized", Needs: api.Resources{CPUMillis: 9000}}
	var normals []api.Job
	for i := 0; i < 3; i++ {
		normals = append(normals, api.Job{ID: api.JobID(fmt.Sprintf("normal-%d", i)), Needs: api.Resources{CPUMillis: 100}})
	}
	worker := api.Worker{ID: "worker-0", Capacity: api.Resources{CPUMillis: 1000}}

	clock := &simulation.Clock{}
	queue := simulation.NewEventQueue()
	exec := simulation.NewExecutor(clock, queue, 1000)
	sched := scheduler.NewScheduler(scheduler.FIFO{}, clock, exec)

	// Submitted before any worker is registered: admissible can't evaluate
	// Needs against an empty pool, so this is provisionally admitted.
	queue.Push(api.JobSubmitted{Job: oversized, Time: 0})
	// The pool now exists, and it's too small for oversized.
	queue.Push(api.WorkerRegistered{Worker: worker, Time: 1})
	for i, j := range normals {
		queue.Push(api.JobSubmitted{Job: j, Time: api.Time(2 + i)})
	}

	simulation.Run(sched, clock, queue)
	log := sched.DecisionLog()

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
		t.Errorf("oversized job rejected %d times, want exactly 1 — WorkerRegistered must re-check pending admission", rejectCount[oversized.ID])
	}
	if dispatchCount[oversized.ID] != 0 {
		t.Errorf("oversized job dispatched %d times, want 0", dispatchCount[oversized.ID])
	}
	for _, j := range normals {
		if got := dispatchCount[j.ID]; got != 1 {
			t.Errorf("job %s dispatched %d times, want exactly 1 — a now-unplaceable head job must not starve jobs behind it", j.ID, got)
		}
	}
}
