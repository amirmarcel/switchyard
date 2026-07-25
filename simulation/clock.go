// Package simulation is the discrete-event, logical-time backend: it
// advances time from event to event instead of sleeping, so the same
// scheduler core can be replayed deterministically at scale.
package simulation

import "github.com/amirmarcel/switchyard/api"

// Clock is a mutable logical clock. The sim driver sets it to each event's
// timestamp as it's popped; it never advances on its own.
type Clock struct {
	now api.Time
}

func (c *Clock) Now() api.Time { return c.now }

func (c *Clock) set(t api.Time) { c.now = t }
