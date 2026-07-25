// scenario_test.go: drives the named burst scenario end to end through the
// full ≥5-run harness and report writers. This is this slice's entrypoint
// in place of a CLI (docs/design/benchmark-harness-spec.md, "Package
// placement" / "First task").
package bench_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amirmarcel/switchyard/bench"
	"github.com/amirmarcel/switchyard/scheduler"
)

func TestBurstScenarioReport(t *testing.T) {
	w := bench.BurstScenario()
	resultsDir := t.TempDir()

	hr, err := bench.RunAndReport(w, scheduler.FIFO{}, bench.MinRuns, resultsDir)
	if err != nil {
		t.Fatalf("RunAndReport: %v", err)
	}

	if !hr.Deterministic {
		t.Errorf("burst scenario is not deterministic across %d runs: %v", hr.Runs, hr.Violations)
	}
	if !hr.InvariantsHeld {
		t.Errorf("invariants did not hold across all runs: %v", hr.Violations)
	}
	if hr.Median.Jobs != 200 {
		t.Errorf("Median.Jobs = %d, want 200", hr.Median.Jobs)
	}
	if hr.Median.QueueDelayP50 <= 0 {
		t.Errorf("Median.QueueDelayP50 = %d, want > 0 — a burst scenario should show real queueing", hr.Median.QueueDelayP50)
	}
	// p50 <= p95 <= p99 must hold for any percentile computation over the
	// same sample set.
	if hr.Median.QueueDelayP50 > hr.Median.QueueDelayP95 || hr.Median.QueueDelayP95 > hr.Median.QueueDelayP99 {
		t.Errorf("percentiles not monotonic: p50=%d p95=%d p99=%d", hr.Median.QueueDelayP50, hr.Median.QueueDelayP95, hr.Median.QueueDelayP99)
	}

	path := filepath.Join(resultsDir, "burst.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected JSON result at %s: %v", path, err)
	}
}

func TestRunHarnessRejectsTooFewRuns(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected RunHarness to panic when runs < MinRuns")
		}
	}()
	bench.RunHarness(bench.BurstScenario(), scheduler.FIFO{}, bench.MinRuns-1)
}
