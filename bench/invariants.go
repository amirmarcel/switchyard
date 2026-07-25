// invariants.go: per-run checks over one benchmark's decision log, mirroring
// the invariants scheduler_test.go proves for the seam itself (see
// CLAUDE.md's non-negotiable invariants). A benchmark run that violates one
// of these is not a valid measurement, so CheckInvariants feeds directly
// into Result.InvariantsHeld.
package bench

import (
	"fmt"
	"sort"

	"github.com/amirmarcel/switchyard/api"
)

// CheckInvariants reconstructs, purely from the decision log, whether the
// run held the scheduler's correctness contract: every job eventually
// dispatched-or-rejected (no starvation), no job dispatched more than once
// (at-most-once), and worker capacity never exceeded. Determinism is
// checked separately, across runs, by the harness (bench/harness.go) —
// it isn't observable from a single log.
func CheckInvariants(w Workload, log []api.Decision) []string {
	var violations []string

	dispatchCount := make(map[api.JobID]int, len(w.Jobs))
	rejectCount := make(map[api.JobID]int, len(w.Jobs))
	for _, d := range log {
		switch d.Outcome {
		case api.Dispatch:
			dispatchCount[d.Job]++
		case api.Reject:
			rejectCount[d.Job]++
		}
	}

	for _, tj := range w.Jobs {
		id := tj.Job.ID
		dispatches, rejects := dispatchCount[id], rejectCount[id]
		switch {
		case dispatches == 0 && rejects == 0:
			violations = append(violations, fmt.Sprintf("job %s never dispatched or rejected (starvation)", id))
		case dispatches > 1:
			violations = append(violations, fmt.Sprintf("job %s dispatched %d times (at-most-once violated)", id, dispatches))
		case dispatches > 0 && rejects > 0:
			violations = append(violations, fmt.Sprintf("job %s both dispatched and rejected", id))
		}
	}

	violations = append(violations, checkCapacityInvariant(w, log)...)

	return violations
}

type interval struct {
	start, end api.Time
	needs      api.Resources
}

// checkCapacityInvariant asserts no worker's dispatched jobs ever overlap
// beyond its capacity, using each dispatch's fixed w.JobDuration as its
// occupancy window — the same interval-overlap reconstruction
// scheduler_test.go's assertNoCapacityViolation uses.
func checkCapacityInvariant(w Workload, log []api.Decision) []string {
	var violations []string

	jobNeeds := make(map[api.JobID]api.Resources, len(w.Jobs))
	for _, tj := range w.Jobs {
		jobNeeds[tj.Job.ID] = tj.Job.Needs
	}
	workerCap := make(map[api.WorkerID]api.Resources, len(w.Workers))
	for _, wk := range w.Workers {
		workerCap[wk.ID] = wk.Capacity
	}

	byWorker := make(map[api.WorkerID][]interval)
	for _, d := range log {
		if d.Outcome != api.Dispatch {
			continue
		}
		byWorker[d.Worker] = append(byWorker[d.Worker], interval{
			start: d.At,
			end:   d.At + w.JobDuration,
			needs: jobNeeds[d.Job],
		})
	}

	workerIDs := make([]api.WorkerID, 0, len(byWorker))
	for id := range byWorker {
		workerIDs = append(workerIDs, id)
	}
	sort.Slice(workerIDs, func(i, j int) bool { return workerIDs[i] < workerIDs[j] })

	for _, id := range workerIDs {
		intervals := byWorker[id]
		cap := workerCap[id]
		// Workers bin-pack: several jobs can be concurrently dispatched to
		// one worker as long as their combined Needs fit its capacity, so
		// pairwise "do these two intervals overlap at all" is not the right
		// test — two jobs can each overlap a third without ever being
		// concurrent with each other. The maximum concurrent load can only
		// change at an interval's start, so it's enough to sum, at each
		// interval's start point t, every interval that actually contains
		// t (start <= t < end).
		for i := range intervals {
			t := intervals[i].start
			var used api.Resources
			for j := range intervals {
				if intervals[j].start <= t && t < intervals[j].end {
					used.CPUMillis += intervals[j].needs.CPUMillis
					used.MemBytes += intervals[j].needs.MemBytes
				}
			}
			if used.CPUMillis > cap.CPUMillis || used.MemBytes > cap.MemBytes {
				violations = append(violations, fmt.Sprintf(
					"capacity exceeded on worker %s at t=%d: used %+v > capacity %+v", id, t, used, cap))
			}
		}
	}

	return violations
}
