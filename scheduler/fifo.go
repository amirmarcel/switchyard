package scheduler

import "github.com/amirmarcel/switchyard/api"

// FIFO dispatches the oldest pending job to the first free-capacity worker
// (workers considered in stable ID order), and Holds — head-of-line — once
// the oldest job can't fit anywhere, rather than letting a younger job jump
// the queue. It is pure: it only reads the State and Time it is given.
type FIFO struct{}

func (FIFO) Name() string { return "fifo" }

func (FIFO) Schedule(s api.State, now api.Time) []api.Decision {
	pending := s.Pending()
	workers := s.Workers()

	free := make(map[api.WorkerID]api.Resources, len(workers))
	for _, w := range workers {
		free[w.ID] = s.FreeCapacity(w.ID)
	}

	var decisions []api.Decision
	for _, job := range pending {
		worker, ok := firstFit(workers, free, job.Needs)
		if !ok {
			decisions = append(decisions, api.Decision{
				Outcome:    api.Hold,
				Job:        job.ID,
				Policy:     "fifo",
				Factors:    map[string]float64{"no_capacity": 1},
				QueueDelay: now - job.SubmitAt,
				At:         now,
			})
			break
		}

		decisions = append(decisions, api.Decision{
			Outcome:    api.Dispatch,
			Job:        job.ID,
			Worker:     worker,
			Policy:     "fifo",
			QueueDelay: now - job.SubmitAt,
			At:         now,
		})

		fc := free[worker]
		fc.CPUMillis -= job.Needs.CPUMillis
		fc.MemBytes -= job.Needs.MemBytes
		free[worker] = fc
	}

	return decisions
}

func firstFit(workers []api.Worker, free map[api.WorkerID]api.Resources, needs api.Resources) (api.WorkerID, bool) {
	for _, w := range workers {
		fc := free[w.ID]
		if fc.CPUMillis >= needs.CPUMillis && fc.MemBytes >= needs.MemBytes {
			return w.ID, true
		}
	}
	return "", false
}
