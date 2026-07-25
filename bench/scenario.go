package bench

import "github.com/amirmarcel/switchyard/api"

// BurstScenario is the one canonical, named scenario this slice ships: a
// fixed 8-worker pool taking a burst of 200 jobs that all arrive inside a
// window much shorter than a single job's execution time, so the queue
// backs up and the scheduler's queue-delay behavior actually gets
// exercised. It becomes the first entry in what will grow into a
// bench/scenarios/ set (docs/design/benchmark-harness-spec.md).
func BurstScenario() Workload {
	return GenerateBurst("burst", BurstParams{
		JobCount:       200,
		WorkerCount:    8,
		WorkerCapacity: api.Resources{CPUMillis: 2000, MemBytes: 4 << 30},

		JobCPUMin: 250, JobCPUMax: 1000,
		JobMemMin: 256 << 20, JobMemMax: 1 << 30,

		ArrivalWindow: 2000, // all 200 jobs arrive within this window
		JobDuration:   5000, // each job then holds its worker for far longer than the window

		Seed: 1337,
	})
}
