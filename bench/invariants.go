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

	violations = append(violations, CheckCapacityInvariant(w, log)...)
	violations = append(violations, checkWorkConservation(w, log)...)

	return violations
}

type interval struct {
	start, end api.Time
	needs      api.Resources
}

// CheckCapacityInvariant asserts no worker's dispatched jobs ever overlap
// beyond its capacity, using each dispatch's fixed w.JobDuration as its
// occupancy window. It is exported so scheduler_test.go can assert the same
// capacity invariant from the scheduler package's own tests rather than
// maintaining a second, divergent implementation (see
// docs/reviews/2026-07-25-opus-code-review.md, finding G1 — the pairwise
// overlap checker that shadowed this one and has since been deleted).
func CheckCapacityInvariant(w Workload, log []api.Decision) []string {
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

// checkWorkConservation enforces CLAUDE.md's conditional work-conservation
// invariant: a Hold is legal only if it's genuinely no-capacity, or it's a
// declared reservation (FIFO's head_of_line_reserved — see
// docs/adr/0004-fifo-non-work-conservation.md). A Hold whose Factors claim
// only "no_capacity" is flagged if, at that instant, some worker actually
// had free room a still-pending job would fit — capacity existed and the
// hold didn't say why it wasn't used. Any other Factors key is treated as a
// declared reservation and passes unconditionally, so this also covers
// future policies that reserve for reasons other than head-of-line.
func checkWorkConservation(w Workload, log []api.Decision) []string {
	var violations []string

	jobNeeds := make(map[api.JobID]api.Resources, len(w.Jobs))
	submitAt := make(map[api.JobID]api.Time, len(w.Jobs))
	for _, tj := range w.Jobs {
		jobNeeds[tj.Job.ID] = tj.Job.Needs
		submitAt[tj.Job.ID] = tj.SubmitAt
	}
	workerCap := make(map[api.WorkerID]api.Resources, len(w.Workers))
	for _, wk := range w.Workers {
		workerCap[wk.ID] = wk.Capacity
	}

	// resolvedAt records the log index at which a job first left the
	// pending set (dispatched or rejected), so pending-at-a-given-index can
	// be reconstructed without needing completions in the log.
	resolvedAt := make(map[api.JobID]int, len(w.Jobs))
	for i, d := range log {
		if d.Outcome != api.Dispatch && d.Outcome != api.Reject {
			continue
		}
		if _, ok := resolvedAt[d.Job]; !ok {
			resolvedAt[d.Job] = i
		}
	}

	for i, d := range log {
		if d.Outcome != api.Hold || declaresReservation(d.Factors) {
			continue
		}

		free := freeCapacityAt(log, i, workerCap, jobNeeds, w.JobDuration, d.At)
		for _, tj := range w.Jobs {
			id := tj.Job.ID
			if submitAt[id] > d.At {
				continue
			}
			if r, ok := resolvedAt[id]; ok && r < i {
				continue
			}
			needs := jobNeeds[id]
			for _, wk := range w.Workers {
				fc := free[wk.ID]
				if fc.CPUMillis >= needs.CPUMillis && fc.MemBytes >= needs.MemBytes {
					violations = append(violations, fmt.Sprintf(
						"unexplained hold for job %s at t=%d (Factors=%v): pending job %s fits worker %s (free %+v)",
						d.Job, d.At, d.Factors, id, wk.ID, fc))
				}
			}
		}
	}

	return violations
}

// declaresReservation reports whether Factors names anything besides
// "no_capacity" — the checker's only legal reason to hold while capacity
// exists. Order-independent existence check, so ranging the map directly
// doesn't threaten determinism.
func declaresReservation(factors map[string]float64) bool {
	for k, v := range factors {
		if k != "no_capacity" && v != 0 {
			return true
		}
	}
	return false
}

// freeCapacityAt reconstructs each worker's free capacity at time at, using
// only Dispatch decisions that appear before index uptoIdx in log — i.e.
// decisions already enacted by the time the Hold at uptoIdx was produced,
// including earlier decisions from the same policy call.
func freeCapacityAt(log []api.Decision, uptoIdx int, workerCap map[api.WorkerID]api.Resources, jobNeeds map[api.JobID]api.Resources, duration api.Time, at api.Time) map[api.WorkerID]api.Resources {
	used := make(map[api.WorkerID]api.Resources, len(workerCap))
	for i := 0; i < uptoIdx; i++ {
		d := log[i]
		if d.Outcome != api.Dispatch {
			continue
		}
		if d.At <= at && at < d.At+duration {
			u := used[d.Worker]
			n := jobNeeds[d.Job]
			u.CPUMillis += n.CPUMillis
			u.MemBytes += n.MemBytes
			used[d.Worker] = u
		}
	}
	free := make(map[api.WorkerID]api.Resources, len(workerCap))
	for id, cap := range workerCap {
		u := used[id]
		free[id] = api.Resources{CPUMillis: cap.CPUMillis - u.CPUMillis, MemBytes: cap.MemBytes - u.MemBytes}
	}
	return free
}
