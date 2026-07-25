package bench_test

import (
	"reflect"
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/bench"
)

func testParams() bench.BurstParams {
	return bench.BurstParams{
		JobCount:       50,
		WorkerCount:    4,
		WorkerCapacity: burstWorkerCap,
		JobCPUMin:      100, JobCPUMax: 800,
		JobMemMin: 1 << 20, JobMemMax: 1 << 21,
		ArrivalWindow: 1000,
		JobDuration:   500,
		Seed:          42,
	}
}

// TestGenerateBurstDeterministic asserts the generator is a pure function
// of its seed and params: same inputs must produce byte-identical
// workloads, since the whole harness's comparability depends on replaying
// (not regenerating) a workload across runs (ADR-0002).
func TestGenerateBurstDeterministic(t *testing.T) {
	w1 := bench.GenerateBurst("burst-test", testParams())
	w2 := bench.GenerateBurst("burst-test", testParams())

	if !reflect.DeepEqual(w1, w2) {
		t.Fatalf("GenerateBurst is not deterministic for the same seed and params:\n%+v\n%+v", w1, w2)
	}
	if len(w1.Jobs) != 50 {
		t.Fatalf("expected 50 jobs, got %d", len(w1.Jobs))
	}
}

// TestGenerateBurstDifferentSeed asserts different seeds actually produce
// different workloads — otherwise determinism would be trivially true
// because the generator ignores its seed.
func TestGenerateBurstDifferentSeed(t *testing.T) {
	p1 := testParams()
	p2 := testParams()
	p2.Seed = 43

	w1 := bench.GenerateBurst("burst-test", p1)
	w2 := bench.GenerateBurst("burst-test", p2)

	if reflect.DeepEqual(w1.Jobs, w2.Jobs) {
		t.Fatal("workloads generated from different seeds are identical — generator ignores its seed")
	}
}

// TestGenerateBurstSortedBySubmitAt asserts the workload's jobs come out
// ordered by SubmitAt, as the Workload doc comment promises.
func TestGenerateBurstSortedBySubmitAt(t *testing.T) {
	w := bench.GenerateBurst("burst-test", testParams())
	for i := 1; i < len(w.Jobs); i++ {
		if w.Jobs[i].SubmitAt < w.Jobs[i-1].SubmitAt {
			t.Fatalf("jobs not sorted by SubmitAt at index %d: %d < %d", i, w.Jobs[i].SubmitAt, w.Jobs[i-1].SubmitAt)
		}
	}
}

var burstWorkerCap = api.Resources{CPUMillis: 2000, MemBytes: 1 << 30}
