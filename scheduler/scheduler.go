// Package scheduler is the backend-agnostic core described in
// docs/design/scheduler-seam-spec.md: it owns the pending queue, worker
// registry, and live assignments, and drives a Policy through an Executor.
// It is single-threaded — exactly one goroutine may call Handle — all
// concurrency lives in the drivers and executors (packages executor and
// simulation), never here, which is what keeps Policy pure and the
// resulting decision log replayable.
package scheduler

import (
	"fmt"
	"sort"

	"github.com/amirmarcel/switchyard/api"
)

type Scheduler struct {
	policy   api.Policy
	clock    api.Clock
	executor api.Executor

	jobs          map[api.JobID]api.Job
	submissionSeq map[api.JobID]int
	nextSeq       int
	completed     map[api.JobID]bool
	running       map[api.JobID]api.Assignment
	workers       map[api.WorkerID]api.Worker
	usedCap       map[api.WorkerID]api.Resources
	leaseSeq      int

	decisionLog []api.Decision
}

func NewScheduler(policy api.Policy, clock api.Clock, executor api.Executor) *Scheduler {
	return &Scheduler{
		policy:        policy,
		clock:         clock,
		executor:      executor,
		jobs:          make(map[api.JobID]api.Job),
		submissionSeq: make(map[api.JobID]int),
		completed:     make(map[api.JobID]bool),
		running:       make(map[api.JobID]api.Assignment),
		workers:       make(map[api.WorkerID]api.Worker),
		usedCap:       make(map[api.WorkerID]api.Resources),
	}
}

// Handle consumes one event, and if it changed schedulability, builds a
// State snapshot, calls policy.Schedule, enacts each Decision via
// executor.Dispatch, and records every Decision to the log.
func (s *Scheduler) Handle(e api.Event) []api.Decision {
	now := s.clock.Now()

	admission, changed := s.apply(e, now)

	var decisions []api.Decision
	if len(admission) > 0 {
		decisions = append(decisions, admission...)
		s.decisionLog = append(s.decisionLog, admission...)
	}
	if !changed {
		return decisions
	}

	scheduled := s.policy.Schedule(s.snapshot(), now)
	for _, d := range scheduled {
		if d.Outcome == api.Dispatch {
			s.enactDispatch(d, now)
		}
		s.decisionLog = append(s.decisionLog, d)
	}
	return append(decisions, scheduled...)
}

// DecisionLog returns a copy of every Decision recorded so far, in order.
func (s *Scheduler) DecisionLog() []api.Decision {
	out := make([]api.Decision, len(s.decisionLog))
	copy(out, s.decisionLog)
	return out
}

// apply folds one event into scheduler state. It returns any admission
// Decisions produced (JobSubmitted rejects at most one; WorkerRegistered
// may reject several pending jobs re-evaluated against the changed pool;
// nil for every other event), and reports whether schedulability may have
// changed — i.e. whether a policy call is needed.
func (s *Scheduler) apply(e api.Event, now api.Time) ([]api.Decision, bool) {
	switch ev := e.(type) {
	case api.JobSubmitted:
		if _, exists := s.jobs[ev.Job.ID]; exists {
			return nil, false
		}
		if !s.admissible(ev.Job) {
			// Rejected at submission, never enters Pending: no
			// registered worker could ever satisfy Needs, so admitting
			// it would let it wedge FIFO's strict head-of-line queue
			// forever. See docs/adr/0001-admission-check-for-unplaceable-jobs.md.
			d := api.Decision{
				Outcome:    api.Reject,
				Job:        ev.Job.ID,
				Policy:     "admission",
				Factors:    map[string]float64{"exceeds_max_worker_capacity": 1},
				QueueDelay: now - ev.Job.SubmitAt,
				At:         now,
			}
			return []api.Decision{d}, false
		}
		s.jobs[ev.Job.ID] = ev.Job
		s.submissionSeq[ev.Job.ID] = s.nextSeq
		s.nextSeq++
		return nil, true

	case api.WorkerRegistered:
		if _, exists := s.workers[ev.Worker.ID]; exists {
			return nil, false
		}
		s.workers[ev.Worker.ID] = ev.Worker
		s.usedCap[ev.Worker.ID] = api.Resources{}
		// A job submitted before any worker existed was provisionally
		// admitted (admissible can't reject what it can't evaluate); now
		// that the pool changed, re-check every still-pending job and
		// reject any that are now provably unplaceable — closing the
		// starvation gap ADR-0001 left open for this ordering. See
		// docs/adr/0001-admission-check-for-unplaceable-jobs.md.
		return s.rejectUnplaceablePending(now), true

	case api.JobCompleted:
		a, ok := s.running[ev.Job]
		if !ok || a.Worker != ev.Worker {
			// Stale or duplicate completion for a job that's no longer
			// (or never was) running under this assignment — ignored.
			// This is the at-most-once safeguard.
			return nil, false
		}
		s.releaseAssignment(a)
		s.completed[ev.Job] = true
		return nil, true

	case api.JobFailed:
		a, ok := s.running[ev.Job]
		if !ok || a.Worker != ev.Worker {
			return nil, false
		}
		s.releaseAssignment(a)
		return nil, true

	case api.CancelRequested:
		if _, ok := s.jobs[ev.Job]; !ok || s.completed[ev.Job] {
			return nil, false
		}
		if a, ok := s.running[ev.Job]; ok {
			_ = s.executor.Cancel(ev.Job)
			s.releaseAssignment(a)
		}
		delete(s.jobs, ev.Job)
		delete(s.submissionSeq, ev.Job)
		return nil, true

	default:
		return nil, false
	}
}

// rejectUnplaceablePending re-runs admissible over every job still in the
// pending set (not completed, not running) in submission order, rejecting
// any that can now be proven unplaceable against the current worker pool.
func (s *Scheduler) rejectUnplaceablePending(now api.Time) []api.Decision {
	ids := make([]api.JobID, 0, len(s.jobs))
	for id := range s.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return s.submissionSeq[ids[i]] < s.submissionSeq[ids[j]] })

	var rejects []api.Decision
	for _, id := range ids {
		if s.completed[id] {
			continue
		}
		if _, running := s.running[id]; running {
			continue
		}
		job := s.jobs[id]
		if s.admissible(job) {
			continue
		}
		rejects = append(rejects, api.Decision{
			Outcome:    api.Reject,
			Job:        id,
			Policy:     "admission",
			Factors:    map[string]float64{"exceeds_max_worker_capacity": 1},
			QueueDelay: now - job.SubmitAt,
			At:         now,
		})
		delete(s.jobs, id)
		delete(s.submissionSeq, id)
	}
	return rejects
}

// admissible reports whether job could ever be dispatched to some
// registered worker. It's a pure existence check — the result doesn't
// depend on which order s.workers is visited in, so iterating the map
// directly here doesn't threaten determinism the way it would inside
// decision-ordering logic. If no worker is registered yet, admissibility
// can't be determined, so the job is provisionally admitted; this is now
// safe rather than a starvation gap, because rejectUnplaceablePending
// re-checks every pending job the moment a worker registers (F2).
func (s *Scheduler) admissible(job api.Job) bool {
	if len(s.workers) == 0 {
		return true
	}
	for _, w := range s.workers {
		if w.Capacity.CPUMillis >= job.Needs.CPUMillis && w.Capacity.MemBytes >= job.Needs.MemBytes {
			return true
		}
	}
	return false
}

func (s *Scheduler) releaseAssignment(a api.Assignment) {
	delete(s.running, a.Job)
	job := s.jobs[a.Job]
	used := s.usedCap[a.Worker]
	used.CPUMillis -= job.Needs.CPUMillis
	used.MemBytes -= job.Needs.MemBytes
	s.usedCap[a.Worker] = used
}

// enactDispatch enforces the capacity invariant before enacting a Dispatch,
// records the assignment, and hands the decision to the executor.
func (s *Scheduler) enactDispatch(d api.Decision, now api.Time) {
	job, ok := s.jobs[d.Job]
	if !ok {
		panic(fmt.Sprintf("scheduler: policy dispatched unknown job %q", d.Job))
	}
	worker, ok := s.workers[d.Worker]
	if !ok {
		panic(fmt.Sprintf("scheduler: policy dispatched to unknown worker %q", d.Worker))
	}
	if _, alreadyRunning := s.running[d.Job]; alreadyRunning {
		panic(fmt.Sprintf("scheduler: policy dispatched already-running job %q (at-most-once violation)", d.Job))
	}

	used := s.usedCap[d.Worker]
	freeCPU := worker.Capacity.CPUMillis - used.CPUMillis
	freeMem := worker.Capacity.MemBytes - used.MemBytes
	if freeCPU < job.Needs.CPUMillis || freeMem < job.Needs.MemBytes {
		panic(fmt.Sprintf("scheduler: capacity invariant violated dispatching job %q to worker %q", d.Job, d.Worker))
	}

	s.leaseSeq++
	assignment := api.Assignment{
		Job:     d.Job,
		Worker:  d.Worker,
		LeaseID: fmt.Sprintf("%s-%d", d.Job, s.leaseSeq),
		StartAt: now,
	}
	s.running[d.Job] = assignment
	used.CPUMillis += job.Needs.CPUMillis
	used.MemBytes += job.Needs.MemBytes
	s.usedCap[d.Worker] = used

	if err := s.executor.Dispatch(d); err != nil {
		panic(fmt.Sprintf("scheduler: executor dispatch failed for job %q: %v", d.Job, err))
	}
}

// snapshot builds a deterministically-ordered State: any map iteration
// below is only used to gather keys, which are always sorted before use.
func (s *Scheduler) snapshot() *snapshot {
	pendingIDs := make([]api.JobID, 0, len(s.jobs))
	for id := range s.jobs {
		if s.completed[id] {
			continue
		}
		if _, running := s.running[id]; running {
			continue
		}
		if !s.depsSatisfied(s.jobs[id]) {
			continue
		}
		pendingIDs = append(pendingIDs, id)
	}
	sort.Slice(pendingIDs, func(i, j int) bool {
		return s.submissionSeq[pendingIDs[i]] < s.submissionSeq[pendingIDs[j]]
	})
	pending := make([]api.Job, len(pendingIDs))
	for i, id := range pendingIDs {
		pending[i] = s.jobs[id]
	}

	workerIDs := make([]api.WorkerID, 0, len(s.workers))
	for id := range s.workers {
		workerIDs = append(workerIDs, id)
	}
	sort.Slice(workerIDs, func(i, j int) bool { return workerIDs[i] < workerIDs[j] })
	workers := make([]api.Worker, len(workerIDs))
	freeCap := make(map[api.WorkerID]api.Resources, len(workerIDs))
	for i, id := range workerIDs {
		w := s.workers[id]
		workers[i] = w
		used := s.usedCap[id]
		freeCap[id] = api.Resources{
			CPUMillis: w.Capacity.CPUMillis - used.CPUMillis,
			MemBytes:  w.Capacity.MemBytes - used.MemBytes,
		}
	}

	runningIDs := make([]api.JobID, 0, len(s.running))
	for id := range s.running {
		runningIDs = append(runningIDs, id)
	}
	sort.Slice(runningIDs, func(i, j int) bool { return runningIDs[i] < runningIDs[j] })
	running := make([]api.Assignment, len(runningIDs))
	for i, id := range runningIDs {
		running[i] = s.running[id]
	}

	return &snapshot{pending: pending, workers: workers, running: running, freeCap: freeCap}
}

func (s *Scheduler) depsSatisfied(job api.Job) bool {
	for _, dep := range job.Deps {
		if !s.completed[dep] {
			return false
		}
	}
	return true
}
