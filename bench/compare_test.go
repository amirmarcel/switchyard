// compare_test.go: the comparison docs/design/candidate-policy-spec.md is
// for — runs the same extended burst scenario (see BurstScenario, now
// carrying cache keys and priorities) through the ≥5-run harness under both
// FIFO and priority+affinity, and reports the p99 queue-delay delta plus the
// affinity hit-rate, alongside every invariant (including work-conservation)
// holding under both. This is the slice's benchmark-comparison entrypoint,
// in place of a CLI (see scenario_test.go for the FIFO-only precedent).
package bench

import (
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
)

// TestBurstScenarioFIFOVsPriorityAffinity runs the harness for both policies
// against the identical, replayed burst workload and prints the headline
// comparison: p99 queue-delay delta and priority+affinity's cache-affinity
// hit-rate (the fraction of its dispatches landing on a worker already warm
// on at least one of the job's cache keys).
func TestBurstScenarioFIFOVsPriorityAffinity(t *testing.T) {
	w := BurstScenario()
	resultsDir := t.TempDir()

	fifoResult := RunHarness(w, scheduler.FIFO{}, MinRuns)
	if err := WriteJSON(resultsDir, "burst-fifo", fifoResult); err != nil {
		t.Fatalf("writing fifo result: %v", err)
	}
	if !fifoResult.Deterministic || !fifoResult.InvariantsHeld {
		t.Fatalf("fifo run: deterministic=%t invariantsHeld=%t violations=%v", fifoResult.Deterministic, fifoResult.InvariantsHeld, fifoResult.Violations)
	}

	affinityResult := RunHarness(w, scheduler.PriorityAffinity{}, MinRuns)
	if err := WriteJSON(resultsDir, "burst-priority-affinity", affinityResult); err != nil {
		t.Fatalf("writing priority-affinity result: %v", err)
	}
	if !affinityResult.Deterministic || !affinityResult.InvariantsHeld {
		t.Fatalf("priority-affinity run: deterministic=%t invariantsHeld=%t violations=%v", affinityResult.Deterministic, affinityResult.InvariantsHeld, affinityResult.Violations)
	}

	// affinityHitRate is computed from one extra representative run (not one
	// of the harness's median-selected runs) purely to report the metric;
	// it doesn't affect InvariantsHeld or the reported percentiles above.
	_, log := run(w, scheduler.PriorityAffinity{})
	hitRate := affinityHitRate(log)

	p99FIFO := fifoResult.Median.QueueDelayP99
	p99Affinity := affinityResult.Median.QueueDelayP99
	var deltaPct float64
	if p99FIFO > 0 {
		deltaPct = 100 * float64(p99FIFO-p99Affinity) / float64(p99FIFO)
	}

	t.Logf("burst scenario: fifo p99=%d  priority-affinity p99=%d  delta=%.1f%%  affinity hit-rate=%.1f%%",
		p99FIFO, p99Affinity, deltaPct, hitRate*100)
	t.Logf("fifo:             p50=%d p95=%d p99=%d throughput=%.4f utilization=%.4f",
		fifoResult.Median.QueueDelayP50, fifoResult.Median.QueueDelayP95, fifoResult.Median.QueueDelayP99,
		fifoResult.Median.ThroughputPerTick, fifoResult.Median.WorkerUtilization)
	t.Logf("priority-affinity: p50=%d p95=%d p99=%d throughput=%.4f utilization=%.4f",
		affinityResult.Median.QueueDelayP50, affinityResult.Median.QueueDelayP95, affinityResult.Median.QueueDelayP99,
		affinityResult.Median.ThroughputPerTick, affinityResult.Median.WorkerUtilization)

	if p99Affinity >= p99FIFO {
		t.Errorf("expected priority+affinity to reduce p99 queue delay vs FIFO on this cache-overlapping burst, got fifo=%d affinity=%d", p99FIFO, p99Affinity)
	}
	if hitRate <= 0 {
		t.Errorf("expected a positive affinity hit-rate on a burst with cache-key overlap, got %.4f", hitRate)
	}
}

// affinityHitRate is the fraction of priority-affinity Dispatch decisions
// that landed on a worker already warm on at least one of the job's cache
// keys (Factors["warm_keys_matched"] > 0). Dispatches for jobs with no
// CacheKeys don't count toward the denominator — they can't hit or miss.
func affinityHitRate(log []api.Decision) float64 {
	var eligible, hits int
	for _, d := range log {
		if d.Outcome != api.Dispatch {
			continue
		}
		if _, hasKeys := d.Factors["cache_affinity"]; !hasKeys {
			continue
		}
		eligible++
		if d.Factors["warm_keys_matched"] > 0 {
			hits++
		}
	}
	if eligible == 0 {
		return 0
	}
	return float64(hits) / float64(eligible)
}
