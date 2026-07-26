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

// activeDispatch is one still-occupying Dispatch tracked by
// checkWorkConservation's sweep (see below) between the log index it was
// added and the index at which its window is retired.
type activeDispatch struct {
	end    api.Time
	worker api.WorkerID
	needs  api.Resources
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
//
// Free capacity is tracked with a single forward sweep and a running usedCap
// tally, not a per-hold rescan of the whole log: every dispatch shares the
// same w.JobDuration, and log.At is non-decreasing in index order (the
// scheduler appends decisions in real event-processing order), so each
// dispatch's end time (At+duration) is also non-decreasing in the order
// dispatches are added. That makes `active` a plain FIFO — the oldest-added
// entry always expires first — so retiring it costs O(1) amortized per
// dispatch, the same start-point-sweep idea CheckCapacityInvariant already
// uses. A policy that legitimately continues past a miss (like
// PriorityAffinity, unlike FIFO's head-of-line break) produces far more
// Holds than Dispatches; the previous per-hold freeCapacityAt rescan was
// O(holds x log length), which is what made that shape slow. This is O(log
// length) overall for the sweep, plus O(holds x jobs x workers) for the fit
// check below, which was never the bottleneck.
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

	var active []activeDispatch
	head := 0
	usedCap := make(map[api.WorkerID]api.Resources, len(workerCap))

	for i, d := range log {
		// Retire dispatches whose window has fully ended before this
		// decision's instant. Inclusive of the boundary (end == d.At keeps
		// a dispatch active) for the same reason freeCapacityAt used to be:
		// two Schedule calls can share one logical instant, and the earlier
		// genuinely runs before a same-instant completion is processed.
		for head < len(active) && active[head].end < d.At {
			e := active[head]
			u := usedCap[e.worker]
			u.CPUMillis -= e.needs.CPUMillis
			u.MemBytes -= e.needs.MemBytes
			usedCap[e.worker] = u
			head++
		}

		if d.Outcome == api.Hold && !declaresReservation(d.Factors) {
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
					cap := workerCap[wk.ID]
					u := usedCap[wk.ID]
					fc := api.Resources{CPUMillis: cap.CPUMillis - u.CPUMillis, MemBytes: cap.MemBytes - u.MemBytes}
					if fc.CPUMillis >= needs.CPUMillis && fc.MemBytes >= needs.MemBytes {
						violations = append(violations, fmt.Sprintf(
							"unexplained hold for job %s at t=%d (Factors=%v): pending job %s fits worker %s (free %+v)",
							d.Job, d.At, d.Factors, id, wk.ID, fc))
					}
				}
			}
		}

		if d.Outcome == api.Dispatch {
			n := jobNeeds[d.Job]
			u := usedCap[d.Worker]
			u.CPUMillis += n.CPUMillis
			u.MemBytes += n.MemBytes
			usedCap[d.Worker] = u
			active = append(active, activeDispatch{end: d.At + w.JobDuration, worker: d.Worker, needs: n})
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
