package simulation

import (
	"testing"

	"github.com/amirmarcel/switchyard/api"
)

// TestDiscountedDurationGradedByOverlap asserts the core graded-discount
// claim from ADR-0006: a fully warm job gets more discount than a partially
// warm one, a partially warm job gets more discount than a cold one, and a
// cold job (no overlap) gets no discount at all.
func TestDiscountedDurationGradedByOverlap(t *testing.T) {
	const base = api.Time(1000)

	cold := DiscountedDuration(base, 0)
	partial := DiscountedDuration(base, 0.5)
	full := DiscountedDuration(base, 1.0)

	if cold != base {
		t.Errorf("cold (no overlap) duration = %d, want unchanged base %d", cold, base)
	}
	if !(partial < cold) {
		t.Errorf("partial-overlap duration %d should be shorter than cold duration %d", partial, cold)
	}
	if !(full < partial) {
		t.Errorf("full-overlap duration %d should be shorter than partial-overlap duration %d", full, partial)
	}
}

// TestDiscountedDurationBounded asserts the discount never drives duration
// below base * (1 - MaxWarmDiscount), even for a full match or an
// out-of-range overlap fraction — the floor ADR-0006 documents.
func TestDiscountedDurationBounded(t *testing.T) {
	const base = api.Time(1000)
	floor := api.Time(float64(base) * (1 - MaxWarmDiscount))

	for _, overlap := range []float64{1.0, 2.0, 100.0} {
		got := DiscountedDuration(base, overlap)
		if got < floor {
			t.Errorf("DiscountedDuration(%d, %v) = %d, below floor %d", base, overlap, got, floor)
		}
	}
}

// TestDiscountedDurationDeterministic asserts the same inputs always
// produce the same output — no time or randomness involved, required for
// same-seed replay to produce byte-identical decision logs and durations.
func TestDiscountedDurationDeterministic(t *testing.T) {
	const base = api.Time(7777)
	const overlap = 0.375

	want := DiscountedDuration(base, overlap)
	for i := 0; i < 100; i++ {
		if got := DiscountedDuration(base, overlap); got != want {
			t.Fatalf("run %d: DiscountedDuration(%d, %v) = %d, want %d (non-deterministic)", i, base, overlap, got, want)
		}
	}
}

// TestExecutorAppliesWarmDiscount drives the sim Executor directly (the
// same seam scheduler.enactDispatch uses) and asserts a Dispatch decision
// carrying a warm_overlap factor completes sooner than an otherwise
// identical one without it — this is the discount actually wired into the
// simulator's duration model, not just the pure formula above.
func TestExecutorAppliesWarmDiscount(t *testing.T) {
	const base = api.Time(1000)
	clock := &Clock{}
	queue := NewEventQueue()
	exec := NewExecutor(clock, queue, base)

	if err := exec.Dispatch(api.Decision{
		Outcome: api.Dispatch, Job: "cold", Worker: "w0", At: 0,
	}); err != nil {
		t.Fatalf("dispatch cold: %v", err)
	}
	if err := exec.Dispatch(api.Decision{
		Outcome: api.Dispatch, Job: "warm", Worker: "w1", At: 0,
		Factors: map[string]float64{"warm_overlap": 1.0},
	}); err != nil {
		t.Fatalf("dispatch warm: %v", err)
	}

	var coldCompletedAt, warmCompletedAt api.Time
	for queue.Len() > 0 {
		e, _ := queue.Pop()
		jc := e.(api.JobCompleted)
		switch jc.Job {
		case "cold":
			coldCompletedAt = jc.Time
		case "warm":
			warmCompletedAt = jc.Time
		}
	}

	if coldCompletedAt != base {
		t.Errorf("cold job completed at %d, want unchanged base duration %d", coldCompletedAt, base)
	}
	if !(warmCompletedAt < coldCompletedAt) {
		t.Errorf("warm job completed at %d, want sooner than cold job's %d", warmCompletedAt, coldCompletedAt)
	}
}
