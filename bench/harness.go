// harness.go: the methodology enforcement layer from
// docs/design/benchmark-harness-spec.md — a single Run is not a benchmark
// number; MinRuns repeats of the same replayed workload, reduced to a
// median plus spread, is. This is what backs a headline "p99 improved X%"
// claim.
package bench

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/amirmarcel/switchyard/api"
)

// MinRuns is the minimum number of repeats the methodology requires per
// configuration (docs/design/benchmark-harness-spec.md, "Methodology").
const MinRuns = 5

// Spread reports the min/max observed for each headline metric across the
// harness's repeated runs, alongside the Median Result.
type Spread struct {
	QueueDelayP50     [2]api.Time
	QueueDelayP95     [2]api.Time
	QueueDelayP99     [2]api.Time
	ThroughputPerTick [2]float64
	WorkerUtilization [2]float64
}

// HarnessResult is the ≥5-run methodology's output: the median run plus
// its spread, and whether every run's invariants held and every run's
// decision log was identical (the determinism precondition).
type HarnessResult struct {
	Scenario string
	Policy   string
	Runs     int

	Median Result
	Spread Spread

	Deterministic  bool // decision log identical across every run
	InvariantsHeld bool // AND of every run's InvariantsHeld and Deterministic
	Violations     []string
}

// RunHarness runs w through p exactly runs times (runs must be >= MinRuns),
// asserts the runs' decision logs are identical (the determinism
// precondition — same seed, same workload, replayed, must reproduce
// byte-identically), checks invariants on every run, and reduces the runs
// to a median Result plus spread.
func RunHarness(w Workload, p api.Policy, runs int) HarnessResult {
	if runs < MinRuns {
		panic(fmt.Sprintf("bench: RunHarness called with %d runs, minimum is %d", runs, MinRuns))
	}

	results := make([]Result, runs)
	logs := make([][]api.Decision, runs)
	for i := 0; i < runs; i++ {
		results[i], logs[i] = run(w, p)
	}

	var violations []string
	deterministic := true
	for i := 1; i < runs; i++ {
		if !reflect.DeepEqual(logs[0], logs[i]) {
			deterministic = false
			violations = append(violations, fmt.Sprintf("decision log for run %d diverged from run 0 (determinism violated)", i))
		}
	}

	invariantsHeld := deterministic
	for i, r := range results {
		if !r.InvariantsHeld {
			invariantsHeld = false
			for _, v := range r.Violations {
				violations = append(violations, fmt.Sprintf("run %d: %s", i, v))
			}
		}
	}

	median := medianResult(results)

	return HarnessResult{
		Scenario:       w.Name,
		Policy:         p.Name(),
		Runs:           runs,
		Median:         median,
		Spread:         spreadOf(results),
		Deterministic:  deterministic,
		InvariantsHeld: invariantsHeld,
		Violations:     violations,
	}
}

// medianResult sorts a copy of results by QueueDelayP50 — the headline
// metric — and returns the middle run whole, so every reported field
// (throughput, utilization, rejections, ...) comes from one real,
// self-consistent run rather than being averaged field-by-field.
func medianResult(results []Result) Result {
	sorted := make([]Result, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].QueueDelayP50 < sorted[j].QueueDelayP50 })
	return sorted[(len(sorted)-1)/2]
}

func spreadOf(results []Result) Spread {
	s := Spread{
		QueueDelayP50:     [2]api.Time{results[0].QueueDelayP50, results[0].QueueDelayP50},
		QueueDelayP95:     [2]api.Time{results[0].QueueDelayP95, results[0].QueueDelayP95},
		QueueDelayP99:     [2]api.Time{results[0].QueueDelayP99, results[0].QueueDelayP99},
		ThroughputPerTick: [2]float64{results[0].ThroughputPerTick, results[0].ThroughputPerTick},
		WorkerUtilization: [2]float64{results[0].WorkerUtilization, results[0].WorkerUtilization},
	}
	for _, r := range results[1:] {
		s.QueueDelayP50 = minMax(s.QueueDelayP50, r.QueueDelayP50)
		s.QueueDelayP95 = minMax(s.QueueDelayP95, r.QueueDelayP95)
		s.QueueDelayP99 = minMax(s.QueueDelayP99, r.QueueDelayP99)
		s.ThroughputPerTick = minMaxF(s.ThroughputPerTick, r.ThroughputPerTick)
		s.WorkerUtilization = minMaxF(s.WorkerUtilization, r.WorkerUtilization)
	}
	return s
}

func minMax(cur [2]api.Time, v api.Time) [2]api.Time {
	if v < cur[0] {
		cur[0] = v
	}
	if v > cur[1] {
		cur[1] = v
	}
	return cur
}

func minMaxF(cur [2]float64, v float64) [2]float64 {
	if v < cur[0] {
		cur[0] = v
	}
	if v > cur[1] {
		cur[1] = v
	}
	return cur
}
