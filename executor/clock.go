// Package executor holds the real, wall-clock backend from
// docs/design/scheduler-seam-spec.md: currently a fake in-memory executor
// standing in for the future Docker executor, plus its clock and driver
// loop. It satisfies the same api.Executor seam as package simulation's
// backend, so the scheduler core runs unmodified against either.
package executor

import (
	"time"

	"github.com/amirmarcel/switchyard/api"
)

// RealClock reads wall-clock time.
type RealClock struct{}

func (RealClock) Now() api.Time { return api.Time(time.Now().UnixNano()) }
