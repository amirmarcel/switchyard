package scheduler

import (
	"sort"

	"github.com/amirmarcel/switchyard/api"
)

// PriorityAffinity is the candidate policy from
// docs/design/candidate-policy-spec.md and ADR-0003: it orders runnable jobs
// by (priority desc, submit asc, JobID) and, for each, dispatches to the
// free-capacity worker warmest on the job's CacheKeys. Unlike FIFO it is
// work-conserving — it never holds a job that fits some free worker just to
// wait for a warmer one, so every Hold it emits is genuinely "no_capacity"
// (see docs/adr/0004-fifo-non-work-conservation.md for the contrast). It is
// pure: it only reads the State and Time it is given, and it treats warmth
// strictly as the snapshot reports it — a worker that becomes warm from an
// earlier dispatch decided in this same Schedule call is not treated as warm
// for a later job in the same call, since warmth is scheduler-tracked state
// established when a job actually runs, not policy-simulated state.
type PriorityAffinity struct{}

func (PriorityAffinity) Name() string { return "priority-affinity" }

func (PriorityAffinity) Schedule(s api.State, now api.Time) []api.Decision {
	pending := append([]api.Job(nil), s.Pending()...)
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if a.SubmitAt != b.SubmitAt {
			return a.SubmitAt < b.SubmitAt
		}
		return a.ID < b.ID
	})

	workers := s.Workers()
	free := make(map[api.WorkerID]api.Resources, len(workers))
	for _, w := range workers {
		free[w.ID] = s.FreeCapacity(w.ID)
	}

	// dispatches and holds are accumulated separately and concatenated
	// dispatches-first below. This is a log-ordering choice, not a placement
	// one: which job goes to which worker is still decided entirely by the
	// priority-ordered loop above. It matters because bench's
	// checkWorkConservation walks the log index by index, and a work-
	// conserving policy legitimately produces Hold-then-Dispatch within one
	// Schedule call (a high-priority job that doesn't fit, followed by a
	// lower-priority one that does) — evaluated in raw emission order, the
	// checker can't yet see that the later dispatch already claimed the
	// capacity it's asking about, and flags a false "unexplained hold".
	// Emitting every Dispatch before any Hold from the same call makes the
	// log reflect the batch's fully-resolved state, which is what "still
	// pending capacity" should mean for decisions produced atomically from
	// one snapshot. It doesn't change which jobs dispatch, to which worker,
	// or the scheduler's actual behavior — enactDispatch only reads
	// Outcome == Dispatch entries, and dispatches to different workers never
	// interact, so this reordering is purely cosmetic to the log.
	var dispatches, holds []api.Decision
	for _, job := range pending {
		worker, matched, ok := bestAffinityFit(workers, free, job)
		if !ok {
			holds = append(holds, api.Decision{
				Outcome: api.Hold,
				Job:     job.ID,
				Policy:  "priority-affinity",
				// Factors carries only "no_capacity": every hold this
				// work-conserving policy emits is genuinely no-capacity, and
				// bench's checkWorkConservation treats any other key as a
				// declared reservation (docs/adr/0004-fifo-non-work-conservation.md)
				// — adding "priority" here would make it wrongly skip
				// checking real holds for prioritized jobs.
				Factors:    map[string]float64{"no_capacity": 1},
				QueueDelay: now - job.SubmitAt,
				At:         now,
			})
			continue
		}

		factors := map[string]float64{"priority": float64(job.Priority)}
		if len(job.CacheKeys) > 0 {
			// Only recorded when the job actually names CacheKeys — affinity
			// is undefined for a job with none, and bench's hit-rate metric
			// (compare_test.go, affinityHitRate) uses this key's presence to
			// decide which dispatches are eligible to count as a hit or miss.
			factors["cache_affinity"] = float64(matched) / float64(len(job.CacheKeys))
			factors["warm_keys_matched"] = float64(matched)
		}
		dispatches = append(dispatches, api.Decision{
			Outcome:    api.Dispatch,
			Job:        job.ID,
			Worker:     worker,
			Policy:     "priority-affinity",
			Factors:    factors,
			QueueDelay: now - job.SubmitAt,
			At:         now,
		})

		fc := free[worker]
		fc.CPUMillis -= job.Needs.CPUMillis
		fc.MemBytes -= job.Needs.MemBytes
		free[worker] = fc
	}

	return append(dispatches, holds...)
}

// bestAffinityFit picks, among workers with enough free capacity for job,
// the one warm on the most of job.CacheKeys — ties broken by worker ID, so
// the choice is deterministic regardless of workers' input order.
func bestAffinityFit(workers []api.Worker, free map[api.WorkerID]api.Resources, job api.Job) (api.WorkerID, int, bool) {
	var bestID api.WorkerID
	bestMatched := -1
	found := false
	for _, w := range workers {
		fc := free[w.ID]
		if fc.CPUMillis < job.Needs.CPUMillis || fc.MemBytes < job.Needs.MemBytes {
			continue
		}
		matched := countWarmMatches(w.WarmCache, job.CacheKeys)
		if !found || matched > bestMatched || (matched == bestMatched && w.ID < bestID) {
			bestID, bestMatched, found = w.ID, matched, true
		}
	}
	return bestID, bestMatched, found
}

func countWarmMatches(warm, keys []string) int {
	if len(warm) == 0 || len(keys) == 0 {
		return 0
	}
	warmSet := make(map[string]struct{}, len(warm))
	for _, k := range warm {
		warmSet[k] = struct{}{}
	}
	matched := 0
	for _, k := range keys {
		if _, ok := warmSet[k]; ok {
			matched++
		}
	}
	return matched
}
