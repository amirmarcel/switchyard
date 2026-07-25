// Package executor holds the real (wall-clock) backend: a fake in-memory
// executor standing in for the future Docker executor, its clock, and the
// real driver loop.
package executor

import (
	"time"

	"github.com/amirmarcel/switchyard/api"
)

// RealClock reads wall-clock time.
type RealClock struct{}

func (RealClock) Now() api.Time { return api.Time(time.Now().UnixNano()) }
