// Package simulation is the discrete-event, logical-time backend from
// docs/design/scheduler-seam-spec.md: it advances time from event to event
// instead of sleeping, so the same scheduler core that drives real
// execution (package executor) can also be replayed deterministically at
// scale. Time only ever moves forward to the next popped event's
// timestamp — never by sleeping — which is what makes large sim runs fast.
package simulation

import "github.com/amirmarcel/switchyard/api"

// Clock is a mutable logical clock. The sim driver sets it to each event's
// timestamp as it's popped; it never advances on its own.
type Clock struct {
	now api.Time
}

func (c *Clock) Now() api.Time { return c.now }

func (c *Clock) set(t api.Time) { c.now = t }
