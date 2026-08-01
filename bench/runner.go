package bench

import (
	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
	"github.com/amirmarcel/switchyard/simulation"
)

// Result is the exact, structured outcome of one benchmark run.
type Result struct {
	Workload string
	Policy   string
	Seed     int64

	Jobs      int
	Completed int
	Rejected  int // admission rejections (Reject decisions)

	// Exact percentiles computed from raw samples — NOT bucketed.
	QueueDelayP50 api.Time
	QueueDelayP95 api.Time
	QueueDelayP99 api.Time
	// (queue delay = dispatch time - submit time; the metric the scheduler controls)

	ThroughputPerTick float64 // completions per unit logical time
	WorkerUtilization float64 // busy CPU-time / total available CPU-time across the pool

	InvariantsHeld bool
	Violations     []string // empty when InvariantsHeld
}

// Run drives one workload through the sim backend under one policy and
// returns Result. It records every dispatched job's raw queue-delay sample
// and computes exact percentiles from it, then checks the scheduler
// invariants over the run's decision log.
func Run(w Workload, p api.Policy) Result {
	result, _ := run(w, p)
	return result
}

// run is the internal entry point that also returns the decision log, so
// the harness (bench/harness.go) can additionally check determinism across
// repeated runs without re-deriving it from Result.
func run(w Workload, p api.Policy) (Result, []api.Decision) {
	clock := &simulation.Clock{}
	queue := simulation.NewEventQueue()
	exec := simulation.NewExecutor(clock, queue, w.JobDuration)
	sched := scheduler.NewScheduler(p, clock, exec)

	for _, worker := range w.Workers {
		queue.Push(api.WorkerRegistered{Worker: worker, Time: 0})
	}
	for _, tj := range w.Jobs {
		queue.Push(api.JobSubmitted{Job: tj.Job, Time: tj.SubmitAt})
	}

	simulation.Run(sched, clock, queue)
	log := sched.DecisionLog()

	return computeResult(w, p, log), log
}

// computeResult derives every Result field purely from the decision log
// and the workload's base job duration — the same "reconstruct from the
// log" approach scheduler_test.go uses for its invariant checks. This sim
// backend has no failure injection in this slice, so every Dispatch
// eventually completes: Completed == the number of Dispatch decisions. Each
// dispatch's actual duration may be less than the base JobDuration — see
// dispatchDuration (bench/invariants.go) and ADR-0006's warm-cache discount.
func computeResult(w Workload, p api.Policy, log []api.Decision) Result {
	samples := queueDelaySamples(log)

	jobCPU := make(map[api.JobID]int, len(w.Jobs))
	for _, tj := range w.Jobs {
		jobCPU[tj.Job.ID] = tj.Job.Needs.CPUMillis
	}

	var dispatched, rejected int
	var makespan api.Time
	var busyCPUTime float64
	for _, d := range log {
		switch d.Outcome {
		case api.Dispatch:
			dispatched++
			duration := dispatchDuration(w, d)
			if end := d.At + duration; end > makespan {
				makespan = end
			}
			// Workers bin-pack: several jobs can run concurrently on one
			// worker, so utilization is CPU-time consumed vs. CPU-time
			// available, not a slot count — a job doesn't occupy a whole
			// worker just by being dispatched to it.
			busyCPUTime += float64(jobCPU[d.Job]) * float64(duration)
		case api.Reject:
			rejected++
		}
	}

	var totalCPUCapacity int
	for _, worker := range w.Workers {
		totalCPUCapacity += worker.Capacity.CPUMillis
	}

	var throughput, utilization float64
	if makespan > 0 {
		throughput = float64(dispatched) / float64(makespan)
		capacityCPUTime := float64(totalCPUCapacity) * float64(makespan)
		if capacityCPUTime > 0 {
			utilization = busyCPUTime / capacityCPUTime
		}
	}

	violations := CheckInvariants(w, log)

	return Result{
		Workload:  w.Name,
		Policy:    p.Name(),
		Seed:      w.Seed,
		Jobs:      len(w.Jobs),
		Completed: dispatched,
		Rejected:  rejected,

		QueueDelayP50: percentile(samples, 50),
		QueueDelayP95: percentile(samples, 95),
		QueueDelayP99: percentile(samples, 99),

		ThroughputPerTick: throughput,
		WorkerUtilization: utilization,

		InvariantsHeld: len(violations) == 0,
		Violations:     violations,
	}
}
