// Package scheduler is the backend-agnostic core: it owns the pending
// queue, worker registry, and live assignments, and drives a Policy through
// an Executor. It is single-threaded — exactly one goroutine may call
// Handle — all concurrency lives in the drivers and executors.
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
	if !s.apply(e) {
		return nil
	}

	now := s.clock.Now()
	decisions := s.policy.Schedule(s.snapshot(), now)

	for _, d := range decisions {
		if d.Outcome == api.Dispatch {
			s.enactDispatch(d, now)
		}
		s.decisionLog = append(s.decisionLog, d)
	}
	return decisions
}

// DecisionLog returns a copy of every Decision recorded so far, in order.
func (s *Scheduler) DecisionLog() []api.Decision {
	out := make([]api.Decision, len(s.decisionLog))
	copy(out, s.decisionLog)
	return out
}

// apply folds one event into scheduler state and reports whether
// schedulability may have changed (i.e. whether a policy call is needed).
func (s *Scheduler) apply(e api.Event) bool {
	switch ev := e.(type) {
	case api.JobSubmitted:
		if _, exists := s.jobs[ev.Job.ID]; exists {
			return false
		}
		s.jobs[ev.Job.ID] = ev.Job
		s.submissionSeq[ev.Job.ID] = s.nextSeq
		s.nextSeq++
		return true

	case api.WorkerRegistered:
		if _, exists := s.workers[ev.Worker.ID]; exists {
			return false
		}
		s.workers[ev.Worker.ID] = ev.Worker
		s.usedCap[ev.Worker.ID] = api.Resources{}
		return true

	case api.JobCompleted:
		a, ok := s.running[ev.Job]
		if !ok || a.Worker != ev.Worker {
			// Stale or duplicate completion for a job that's no longer
			// (or never was) running under this assignment — ignored.
			// This is the at-most-once safeguard.
			return false
		}
		s.releaseAssignment(a)
		s.completed[ev.Job] = true
		return true

	case api.JobFailed:
		a, ok := s.running[ev.Job]
		if !ok || a.Worker != ev.Worker {
			return false
		}
		s.releaseAssignment(a)
		return true

	case api.CancelRequested:
		if _, ok := s.jobs[ev.Job]; !ok || s.completed[ev.Job] {
			return false
		}
		if a, ok := s.running[ev.Job]; ok {
			_ = s.executor.Cancel(ev.Job)
			s.releaseAssignment(a)
		}
		delete(s.jobs, ev.Job)
		delete(s.submissionSeq, ev.Job)
		return true

	default:
		return false
	}
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
