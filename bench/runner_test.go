// runner_test.go: acceptance-level tests for Run against deliberately
// constructed workloads, complementing bench/scenario_test.go's end-to-end
// coverage of the named burst scenario.
package bench_test

import (
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/bench"
	"github.com/amirmarcel/switchyard/scheduler"
)

// TestRunCapacityStarved deliberately starves the worker pool: far more
// jobs than capacity can hold at once, plus a handful of oversized jobs
// that can never fit any worker. It asserts Run reports the right
// rejection count (admission-time, for the oversized jobs only) and a
// worker utilization close to saturated, since with this much backlog the
// pool should be busy almost the entire run.
func TestRunCapacityStarved(t *testing.T) {
	const normalCount = 40
	const oversizedCount = 3

	w := bench.GenerateBurst("starved", bench.BurstParams{
		JobCount:       normalCount,
		WorkerCount:    2,
		WorkerCapacity: api.Resources{CPUMillis: 1000, MemBytes: 1 << 30},
		JobCPUMin:      1000, JobCPUMax: 1000, // every normal job saturates a whole worker
		JobMemMin: 1 << 20, JobMemMax: 1 << 20,
		ArrivalWindow: 100, // all arrive nearly at once
		JobDuration:   1000,
		Seed:          7,
	})

	// Splice in jobs whose Needs exceed every worker's capacity: the
	// scheduler must reject these at admission (docs/adr/0001), not dispatch
	// or starve them.
	for i := 0; i < oversizedCount; i++ {
		id := api.JobID("starved-oversized-" + string(rune('a'+i)))
		w.Jobs = append(w.Jobs, bench.TimedJob{
			Job:      api.Job{ID: id, Needs: api.Resources{CPUMillis: 5000, MemBytes: 1 << 30}, SubmitAt: 0},
			SubmitAt: 0,
		})
	}

	result := bench.Run(w, scheduler.FIFO{})

	if !result.InvariantsHeld {
		t.Fatalf("invariants violated on capacity-starved run: %v", result.Violations)
	}
	if result.Jobs != normalCount+oversizedCount {
		t.Fatalf("Jobs = %d, want %d", result.Jobs, normalCount+oversizedCount)
	}
	if result.Rejected != oversizedCount {
		t.Fatalf("Rejected = %d, want %d (the oversized jobs, and only them)", result.Rejected, oversizedCount)
	}
	if result.Completed != normalCount {
		t.Fatalf("Completed = %d, want %d", result.Completed, normalCount)
	}
	// 2 workers serving 40 saturating jobs one at a time each, back to back,
	// with a burst arrival: the pool should be almost continuously busy.
	if result.WorkerUtilization < 0.9 {
		t.Errorf("WorkerUtilization = %.4f, want >= 0.9 for a capacity-starved run", result.WorkerUtilization)
	}
	// Every normal job serializes behind the 2-worker pool, so the tail of
	// the queue waits a large multiple of JobDuration — the starvation this
	// scenario is meant to exercise.
	if result.QueueDelayP99 < 10*w.JobDuration {
		t.Errorf("QueueDelayP99 = %d, want >= %d given %d jobs serialized on 2 workers", result.QueueDelayP99, 10*w.JobDuration, normalCount)
	}
}
