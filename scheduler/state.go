package scheduler

import "github.com/amirmarcel/switchyard/api"

// snapshot is a materialized, read-only view of scheduler state, built
// fresh before each policy call so the policy never observes a state that
// changes mid-decision (prefer snapshot over live view for determinism).
type snapshot struct {
	pending []api.Job
	workers []api.Worker
	running []api.Assignment
	freeCap map[api.WorkerID]api.Resources
}

func (s *snapshot) Pending() []api.Job        { return s.pending }
func (s *snapshot) Workers() []api.Worker     { return s.workers }
func (s *snapshot) Running() []api.Assignment { return s.running }

func (s *snapshot) FreeCapacity(id api.WorkerID) api.Resources {
	return s.freeCap[id]
}
