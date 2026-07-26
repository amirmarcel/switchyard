// Package bench is the measurement path: it drives a fixed, seeded
// workload through the sim backend under a Policy and reports exact
// latency percentiles, throughput, and utilization, with the scheduler
// invariants checked on every run. See
// docs/design/benchmark-harness-spec.md and
// docs/adr/0002-exact-percentiles-and-replay-not-regenerate.md for the
// design decisions this package fixes.
package bench

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/amirmarcel/switchyard/api"
)

// Workload is an ordered set of timed job submissions plus the worker pool
// they run on, generated once from a seed and replayed identically across
// every run that references it — never regenerated per run. This is what
// makes runs comparable (see ADR-0002).
type Workload struct {
	Name        string
	Seed        int64
	Workers     []api.Worker
	Jobs        []TimedJob // sorted by SubmitAt, ties broken by generation order
	JobDuration api.Time   // fixed execution duration charged by the sim executor
}

// TimedJob pairs a Job with its submission time. Job.SubmitAt is kept equal
// to SubmitAt so both the scheduler's queue-delay math and the workload
// model agree on when the job arrived.
type TimedJob struct {
	Job      api.Job
	SubmitAt api.Time
}

// BurstParams parameterizes the single reference generator this slice
// ships: a fixed worker pool and a batch of jobs that all arrive within a
// short window, which is the arrival pattern that stresses a scheduler
// (see docs/design/benchmark-harness-spec.md, "The canonical scenario").
// The three named profiles from the README (interactive/batch/agent) are
// out of scope for this slice.
type BurstParams struct {
	JobCount       int
	WorkerCount    int
	WorkerCapacity api.Resources

	JobCPUMin, JobCPUMax int
	JobMemMin, JobMemMax int64

	ArrivalWindow api.Time // jobs arrive uniformly at random within [0, ArrivalWindow)
	JobDuration   api.Time

	// CacheKeyPoolSize and CacheKeysPerJob give jobs affinity to exploit: each
	// job draws CacheKeysPerJob distinct keys from a pool of CacheKeyPoolSize
	// names. A pool much smaller than JobCount is what creates meaningful
	// overlap (many jobs sharing a key) rather than every job being unique.
	// Zero (the default) assigns no CacheKeys, unchanged from before this
	// field existed.
	CacheKeyPoolSize int
	CacheKeysPerJob  int

	// HighPriorityCount jobs (by generation index, independent of the
	// randomized arrival order) get Priority = HighPriority; every other job
	// gets Priority 0. Zero (the default) assigns no priority, unchanged from
	// before this field existed.
	HighPriorityCount int
	HighPriority      int

	Seed int64
}

// GenerateBurst produces a replayable burst Workload: JobCount jobs with
// sizes drawn uniformly from [JobCPUMin,JobCPUMax]x[JobMemMin,JobMemMax],
// arriving at uniformly random times within [0, ArrivalWindow), against a
// pool of WorkerCount identical workers. All randomness comes from an RNG
// seeded by p.Seed — never the math/rand global — so the same name+params
// always produce byte-identical output.
func GenerateBurst(name string, p BurstParams) Workload {
	rng := rand.New(rand.NewSource(p.Seed))

	workers := make([]api.Worker, p.WorkerCount)
	for i := 0; i < p.WorkerCount; i++ {
		workers[i] = api.Worker{
			ID:       api.WorkerID(fmt.Sprintf("%s-worker-%03d", name, i)),
			Capacity: p.WorkerCapacity,
		}
	}

	jobs := make([]TimedJob, p.JobCount)
	for i := 0; i < p.JobCount; i++ {
		var submitAt api.Time
		if p.ArrivalWindow > 0 {
			submitAt = api.Time(rng.Int63n(int64(p.ArrivalWindow)))
		}
		cpu := p.JobCPUMin
		if p.JobCPUMax > p.JobCPUMin {
			cpu += rng.Intn(p.JobCPUMax - p.JobCPUMin + 1)
		}
		mem := p.JobMemMin
		if p.JobMemMax > p.JobMemMin {
			mem += rng.Int63n(p.JobMemMax - p.JobMemMin + 1)
		}
		priority := 0
		if i < p.HighPriorityCount {
			priority = p.HighPriority
		}
		job := api.Job{
			ID:        api.JobID(fmt.Sprintf("%s-job-%04d", name, i)),
			Class:     api.Batch,
			Needs:     api.Resources{CPUMillis: cpu, MemBytes: mem},
			Priority:  priority,
			CacheKeys: cacheKeys(rng, name, p.CacheKeyPoolSize, p.CacheKeysPerJob),
			SubmitAt:  submitAt,
		}
		jobs[i] = TimedJob{Job: job, SubmitAt: submitAt}
	}

	// Stable sort preserves generation order as the tie-break for equal
	// SubmitAt, which is what the scheduler's submission sequence will use
	// once these are pushed onto the event queue in this order — keeping
	// the workload's own ordering deterministic end to end.
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].SubmitAt < jobs[j].SubmitAt })

	return Workload{
		Name:        name,
		Seed:        p.Seed,
		Workers:     workers,
		Jobs:        jobs,
		JobDuration: p.JobDuration,
	}
}

// cacheKeys draws n distinct keys (n = perJob, capped at poolSize) from a
// pool of poolSize keys named "<name>-key-<i>", using rng so the draw stays
// part of the seeded, replayable sequence. A pool smaller than JobCount is
// what makes affinity meaningful: distinct jobs land on the same key and can
// reuse a worker warmed by an earlier one. Returns nil when poolSize or
// perJob is 0 (the default), so callers that don't set these fields keep
// generating jobs with no CacheKeys, unchanged from before this existed.
func cacheKeys(rng *rand.Rand, name string, poolSize, perJob int) []string {
	if poolSize <= 0 || perJob <= 0 {
		return nil
	}
	if perJob > poolSize {
		perJob = poolSize
	}
	chosen := make(map[int]struct{}, perJob)
	for len(chosen) < perJob {
		chosen[rng.Intn(poolSize)] = struct{}{}
	}
	keys := make([]string, 0, perJob)
	for i := range chosen {
		keys = append(keys, fmt.Sprintf("%s-key-%03d", name, i))
	}
	sort.Strings(keys)
	return keys
}
