// invariants_test.go: unit tests on CheckCapacityInvariant itself, built
// from hand-constructed decision logs rather than full scenario runs. They
// pin the fix from pairwise-overlap to a start-point sweep: the old check
// asked "do intervals i and j overlap anywhere in i's span," which
// over-counts when a worker legitimately bin-packs several jobs that
// pairwise-overlap a common long-lived job without ever being concurrent
// with each other. The current check asks "which intervals are actually
// active at this specific instant," which is the question that matters.
package bench

import (
	"strings"
	"testing"

	"github.com/amirmarcel/switchyard/api"
	"github.com/amirmarcel/switchyard/scheduler"
)

const capTestWorker = api.WorkerID("w")

// capTestWorkload builds a single-worker Workload whose Jobs list matches
// the dispatched job IDs 1:1 (each with the given needs), so
// CheckCapacityInvariant sees exactly the jobs it needs and nothing the
// starvation/at-most-once checks in CheckInvariants would also flag.
func capTestWorkload(capacity int, duration api.Time, needs map[api.JobID]int) Workload {
	jobs := make([]TimedJob, 0, len(needs))
	for id, cpu := range needs {
		jobs = append(jobs, TimedJob{Job: api.Job{ID: id, Needs: api.Resources{CPUMillis: cpu}}})
	}
	return Workload{
		Name:        "capacity-test",
		Workers:     []api.Worker{{ID: capTestWorker, Capacity: api.Resources{CPUMillis: capacity}}},
		Jobs:        jobs,
		JobDuration: duration,
	}
}

func dispatchAt(job api.JobID, at api.Time) api.Decision {
	return api.Decision{Outcome: api.Dispatch, Job: job, Worker: capTestWorker, At: at}
}

// TestCapacityInvariantBinPackingWithinCapacity reconstructs the exact
// shape of the real bug: a long-lived job A, and two shorter-overlap jobs
// B and C that each pairwise-overlap A (near opposite ends of A's span)
// but never overlap each other. True concurrent usage never exceeds 2
// (A+C early, A+B late) against a capacity of 2 — this is legitimate
// bin-packing, not a violation.
//
// Under the old pairwise-overlap-with-i's-whole-span check, reference A
// would wrongly sum A+B+C (needs 1+1+1=3 > capacity 2) because B and C
// both overlap *somewhere* in A's span, even though neither is active at
// the same instant as the other. The start-point sweep only sums
// intervals actually active at each reference instant, so it correctly
// finds no instant where usage exceeds capacity.
func TestCapacityInvariantBinPackingWithinCapacity(t *testing.T) {
	const duration = api.Time(100)
	w := capTestWorkload(2, duration, map[api.JobID]int{
		"A": 1, "B": 1, "C": 1,
	})

	log := []api.Decision{
		dispatchAt("A", 1000), // span [1000, 1100)
		dispatchAt("C", 901),  // span [901, 1001)  -- overlaps A near A's start, ends just after it starts
		dispatchAt("B", 1099), // span [1099, 1199) -- overlaps A near A's end, starts just before it ends
	}
	// B and C do not overlap each other: C ends at 1001, B starts at 1099.

	if violations := CheckCapacityInvariant(w, log); len(violations) != 0 {
		t.Fatalf("expected no capacity violation for legitimate bin-packing, got: %v", violations)
	}
}

// TestCapacityInvariantBinPackingOverCapacity is the positive control for
// the test above: four jobs genuinely, simultaneously co-resident on one
// worker (identical dispatch time and duration) whose combined needs
// exceed capacity must be flagged.
func TestCapacityInvariantBinPackingOverCapacity(t *testing.T) {
	const duration = api.Time(100)
	w := capTestWorkload(3, duration, map[api.JobID]int{
		"W": 1, "X": 1, "Y": 1, "Z": 1,
	})

	log := []api.Decision{
		dispatchAt("W", 0),
		dispatchAt("X", 0),
		dispatchAt("Y", 0),
		dispatchAt("Z", 0),
	}

	violations := CheckCapacityInvariant(w, log)
	if len(violations) == 0 {
		t.Fatal("expected a capacity violation: 4 x 1 CPU jobs concurrently on a 3 CPU worker")
	}
}

// TestCapacityInvariantAdjacentBoundary asserts the check is half-open:
// job A occupies the worker up to but not including its end tick, so a
// job B starting exactly when A ends does not overlap it, even though
// their combined needs would exceed capacity if they were concurrent.
func TestCapacityInvariantAdjacentBoundary(t *testing.T) {
	const duration = api.Time(100)
	w := capTestWorkload(1, duration, map[api.JobID]int{
		"A": 1, "B": 1,
	})

	log := []api.Decision{
		dispatchAt("A", 0),   // span [0, 100)
		dispatchAt("B", 100), // span [100, 200) -- starts exactly when A ends
	}

	if violations := CheckCapacityInvariant(w, log); len(violations) != 0 {
		t.Fatalf("expected no violation for adjacent, non-overlapping intervals, got: %v", violations)
	}
}

// TestFIFOPassesWorkConservationChecker runs FIFO on the shipped burst
// scenario — the exact run the review instrumented and found 46% of holds
// non-work-conserving by the letter of the invariant — and asserts every
// Hold now passes checkWorkConservation: FIFO's holds are all declared
// head-of-line reservations (see docs/adr/0004-fifo-non-work-conservation.md),
// which the checker must accept.
func TestFIFOPassesWorkConservationChecker(t *testing.T) {
	w := BurstScenario()
	_, log := run(w, scheduler.FIFO{})

	var holds int
	for _, d := range log {
		if d.Outcome == api.Hold {
			holds++
		}
	}
	if holds == 0 {
		t.Fatal("expected at least one Hold decision on the burst scenario to make this test meaningful")
	}

	if violations := checkWorkConservation(w, log); len(violations) != 0 {
		t.Fatalf("FIFO's declared head-of-line holds should pass the work-conservation checker, got: %v", violations)
	}
}

// TestWorkConservationBoundaryTieNotFlagged pins a fix found while
// validating PriorityAffinity on the burst scenario: two Schedule calls can
// legitimately share the exact same logical instant (e.g. a job's
// completion and some other event both land at t), and the earlier of the
// two is genuinely evaluated *before* that completion is processed — the
// real scheduler still shows the completing job occupying its worker at
// that instant. freeCapacityAt's window must treat the boundary
// (at == dispatch.At+duration) as still-occupying for this reason, unlike
// CheckCapacityInvariant's strictly half-open window (which only needs to
// reason about genuinely distinct instants, never same-instant ties). Before
// this fix, job A's capacity was reconstructed as already free at t=100,
// so holding B (which doesn't actually fit until A's completion is
// processed) was wrongly flagged as an unexplained hold.
func TestWorkConservationBoundaryTieNotFlagged(t *testing.T) {
	const duration = api.Time(100)
	w := capTestWorkload(1000, duration, map[api.JobID]int{
		"A": 600, "B": 500,
	})

	log := []api.Decision{
		dispatchAt("A", 0), // occupies [0,100], including the boundary itself
		{Outcome: api.Hold, Job: "B", Factors: map[string]float64{"no_capacity": 1}, At: 100},
	}

	if violations := checkWorkConservation(w, log); len(violations) != 0 {
		t.Fatalf("expected no violation for a hold at the exact instant a same-tick job's window ends, got: %v", violations)
	}
}

// TestPriorityAffinityPassesWorkConservationChecker proves the affinity
// policy's central claim from docs/design/candidate-policy-spec.md — it is
// work-conserving, so it must never hold a job while a worker it fits is
// free. Runs on the shipped burst scenario (heterogeneous job sizes, cache
// keys, and priorities — see BurstScenario), which is the same run used to
// compute the FIFO-vs-affinity comparison in compare_test.go, so a
// regression here would also silently invalidate that benchmark's
// InvariantsHeld claim.
func TestPriorityAffinityPassesWorkConservationChecker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping whole-scenario affinity work-conservation check in -short mode")
	}
	w := BurstScenario()
	_, log := run(w, scheduler.PriorityAffinity{})

	var holds int
	for _, d := range log {
		if d.Outcome == api.Hold {
			holds++
		}
	}
	if holds == 0 {
		t.Fatal("expected at least one Hold decision on the burst scenario to make this test meaningful")
	}

	if violations := checkWorkConservation(w, log); len(violations) != 0 {
		t.Fatalf("priority-affinity's holds should all be genuine no-capacity holds (it is work-conserving), got: %v", violations)
	}
}

// atMostOnceTestWorkload builds a minimal single-job Workload for exercising
// CheckInvariants' at-most-once check in isolation, against a hand-built
// decision log rather than a full scenario run.
func atMostOnceTestWorkload(job api.JobID) Workload {
	return Workload{
		Name:        "at-most-once-test",
		Workers:     []api.Worker{{ID: "w0", Capacity: api.Resources{CPUMillis: 1000}}},
		Jobs:        []TimedJob{{Job: api.Job{ID: job, Needs: api.Resources{CPUMillis: 1000}}}},
		JobDuration: 1000,
	}
}

// TestAtMostOnceCheckerIgnoresLegitimateRetry pins the F5 fix
// (docs/known-issues.md): a job dispatched twice (an ordinary retry after
// JobFailed) but accepted-completed only once must NOT be flagged as an
// at-most-once violation. Before the fix, CheckInvariants counted
// Dispatch decisions rather than Completed decisions, so this exact shape
// — the normal, mandatory retry behavior CLAUDE.md requires — read as a
// violation on every benchmark with a retry.
func TestAtMostOnceCheckerIgnoresLegitimateRetry(t *testing.T) {
	w := atMostOnceTestWorkload("j")

	log := []api.Decision{
		{Outcome: api.Dispatch, Job: "j", Worker: "w0", LeaseID: "j-1", At: 0},
		{Outcome: api.Fenced, Job: "j", Worker: "w0", LeaseID: "j-1", At: 500}, // e.g. a stale JobFailed, irrelevant here
		// Retried dispatch starts only after run#1's occupancy window
		// ([0, JobDuration)) has ended, so this log doesn't also trip the
		// (unrelated) capacity invariant.
		{Outcome: api.Dispatch, Job: "j", Worker: "w0", LeaseID: "j-2", At: 1000},
		{Outcome: api.Completed, Job: "j", Worker: "w0", LeaseID: "j-2", At: 2000},
	}

	if violations := CheckInvariants(w, log); len(violations) != 0 {
		t.Fatalf("dispatched-twice-completed-once must not violate at-most-once, got: %v", violations)
	}
}

// TestAtMostOnceCheckerFlagsTwoAcceptedCompletions is the positive control:
// two accepted (non-fenced) completions for the same job is a genuine
// at-most-once violation and must be flagged, regardless of dispatch count.
func TestAtMostOnceCheckerFlagsTwoAcceptedCompletions(t *testing.T) {
	w := atMostOnceTestWorkload("j")

	log := []api.Decision{
		{Outcome: api.Dispatch, Job: "j", Worker: "w0", LeaseID: "j-1", At: 0},
		{Outcome: api.Completed, Job: "j", Worker: "w0", LeaseID: "j-1", At: 10},
		{Outcome: api.Completed, Job: "j", Worker: "w0", LeaseID: "j-1", At: 20},
	}

	violations := CheckInvariants(w, log)
	if len(violations) == 0 {
		t.Fatal("expected an at-most-once violation for two accepted completions of the same job")
	}
	found := false
	for _, v := range violations {
		if strings.Contains(v, "at-most-once violated") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an at-most-once violation among %v", violations)
	}
}

// badHoldPolicy always holds the head pending job, mislabeled as
// "no_capacity" regardless of whether capacity actually exists — the
// deliberately-bad policy checkWorkConservation must catch (it never
// declares a reservation, per docs/adr/0004-fifo-non-work-conservation.md).
type badHoldPolicy struct{}

func (badHoldPolicy) Name() string { return "bad-hold" }

func (badHoldPolicy) Schedule(s api.State, now api.Time) []api.Decision {
	pending := s.Pending()
	if len(pending) == 0 {
		return nil
	}
	job := pending[0]
	return []api.Decision{{
		Outcome:    api.Hold,
		Job:        job.ID,
		Policy:     "bad-hold",
		Factors:    map[string]float64{"no_capacity": 1},
		QueueDelay: now - job.SubmitAt,
		At:         now,
	}}
}

// TestBadHoldPolicyFlaggedByWorkConservationChecker is the positive
// control: a job that fits comfortably in a fully free worker, held anyway
// under a false "no_capacity" claim with no declared reservation, must be
// flagged.
func TestBadHoldPolicyFlaggedByWorkConservationChecker(t *testing.T) {
	w := Workload{
		Name:        "bad-hold-test",
		Workers:     []api.Worker{{ID: "w0", Capacity: api.Resources{CPUMillis: 1000}}},
		Jobs:        []TimedJob{{Job: api.Job{ID: "j0", Needs: api.Resources{CPUMillis: 500}, SubmitAt: 0}, SubmitAt: 0}},
		JobDuration: 1000,
	}

	_, log := run(w, badHoldPolicy{})

	if violations := checkWorkConservation(w, log); len(violations) == 0 {
		t.Fatal("expected the work-conservation checker to flag an unexplained hold while capacity existed")
	}
}
