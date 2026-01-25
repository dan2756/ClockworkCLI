// Package schedule implements the kinds of schedules cronlite understands.
package schedule

import "time"

// A Schedule computes when a task fires.
type Schedule interface {
	// Next returns the first fire time strictly after t, in t's location.
	// It returns the zero time if the schedule never fires again.
	Next(t time.Time) time.Time
	// String returns the schedule in task file syntax.
	String() string
}
