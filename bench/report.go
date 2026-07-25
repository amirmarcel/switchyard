// report.go: the two output forms the spec requires from the same
// HarnessResult — structured JSON for a later plotting script to consume,
// and a readable stdout summary for the "runs and shows a number"
// experience. No plotting or CLI here; both are later slices.
package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amirmarcel/switchyard/api"
)

// WriteJSON marshals v to indented JSON and writes it to
// <resultsDir>/<scenario>.json, creating resultsDir if needed.
func WriteJSON(resultsDir, scenario string, v any) error {
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return fmt.Errorf("bench: creating results dir %s: %w", resultsDir, err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: marshaling result for %s: %w", scenario, err)
	}
	path := filepath.Join(resultsDir, scenario+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("bench: writing %s: %w", path, err)
	}
	return nil
}

// PrintSummary prints the "runs and shows a number" stdout report for a
// HarnessResult: scenario, policy, jobs, completed/rejected, the median
// run's p50/p95/p99 queue delay, throughput, utilization, and whether
// invariants held across every run.
func PrintSummary(hr HarnessResult) {
	m := hr.Median
	fmt.Printf("scenario: %s  policy: %s  runs: %d\n", hr.Scenario, hr.Policy, hr.Runs)
	fmt.Printf("jobs: %d  completed: %d  rejected: %d\n", m.Jobs, m.Completed, m.Rejected)
	fmt.Printf("queue delay (median run)  p50: %d  p95: %d  p99: %d\n", m.QueueDelayP50, m.QueueDelayP95, m.QueueDelayP99)
	fmt.Printf("queue delay p50 spread: [%d, %d]  p95 spread: [%d, %d]  p99 spread: [%d, %d]\n",
		hr.Spread.QueueDelayP50[0], hr.Spread.QueueDelayP50[1],
		hr.Spread.QueueDelayP95[0], hr.Spread.QueueDelayP95[1],
		hr.Spread.QueueDelayP99[0], hr.Spread.QueueDelayP99[1])
	fmt.Printf("throughput/tick: %.4f (spread [%.4f, %.4f])\n", m.ThroughputPerTick, hr.Spread.ThroughputPerTick[0], hr.Spread.ThroughputPerTick[1])
	fmt.Printf("worker utilization: %.4f (spread [%.4f, %.4f])\n", m.WorkerUtilization, hr.Spread.WorkerUtilization[0], hr.Spread.WorkerUtilization[1])
	fmt.Printf("deterministic across runs: %t  invariants held: %t\n", hr.Deterministic, hr.InvariantsHeld)
	if !hr.InvariantsHeld {
		fmt.Println("violations:")
		for _, v := range hr.Violations {
			fmt.Printf("  - %s\n", v)
		}
	}
}

// RunAndReport runs the ≥5-run harness for w against p, prints the stdout
// summary, and writes the HarnessResult as JSON to resultsDir. It is the
// programmatic entrypoint this slice ships in place of a CLI (the CLI
// graduates later, per docs/design/benchmark-harness-spec.md).
func RunAndReport(w Workload, p api.Policy, runs int, resultsDir string) (HarnessResult, error) {
	hr := RunHarness(w, p, runs)
	PrintSummary(hr)
	if err := WriteJSON(resultsDir, hr.Scenario, hr); err != nil {
		return hr, err
	}
	return hr, nil
}
