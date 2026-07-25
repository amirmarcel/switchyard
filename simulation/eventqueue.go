package simulation

import "container/heap"

import "github.com/amirmarcel/switchyard/api"

// EventQueue is a time-ordered priority queue of events, ties broken by
// insertion sequence number so ordering is total and deterministic — this
// backs the determinism invariant across sim reruns.
type EventQueue struct {
	items   eventHeap
	nextSeq int64
}

func NewEventQueue() *EventQueue {
	return &EventQueue{}
}

func (q *EventQueue) Push(e api.Event) {
	heap.Push(&q.items, queuedEvent{event: e, seq: q.nextSeq})
	q.nextSeq++
}

func (q *EventQueue) Pop() (api.Event, bool) {
	if q.items.Len() == 0 {
		return nil, false
	}
	qe := heap.Pop(&q.items).(queuedEvent)
	return qe.event, true
}

func (q *EventQueue) Len() int { return q.items.Len() }

type queuedEvent struct {
	event api.Event
	seq   int64
}

type eventHeap []queuedEvent

func (h eventHeap) Len() int { return len(h) }

func (h eventHeap) Less(i, j int) bool {
	if h[i].event.At() != h[j].event.At() {
		return h[i].event.At() < h[j].event.At()
	}
	return h[i].seq < h[j].seq
}

func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *eventHeap) Push(x any) { *h = append(*h, x.(queuedEvent)) }

func (h *eventHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
