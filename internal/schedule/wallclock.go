package schedule

import "time"

// Schedules are defined on the wall clock: "at 09:00" means whenever the
// local clock reads 09:00. The search for the next fire time therefore runs
// over wall clock readings, kept as UTC times (where every day has 24
// hours), and only the final answer is turned into an instant in the
// schedule's location.

// civil returns t's wall clock reading as a UTC time.
func civil(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// inZone returns the instant in loc at which the wall clock reads w, a
// whole minute from civil. Daylight saving time makes that ambiguous twice
// a year:
//
//   - When clocks spring forward, readings in the gap never happen. They
//     map to the moment of the jump, so a 02:30 job runs at 03:00 that day
//     instead of being skipped.
//   - When clocks fall back, readings in the repeated hour happen twice.
//     They map to the first occurrence, so a 01:30 job runs once.
func inZone(w time.Time, loc *time.Location) time.Time {
	t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, loc)
	if got := civil(t); !got.Equal(w) {
		// w is in a gap and time.Date has pushed it to one side or the
		// other (which side depends on the zone). Either way the jump is
		// at the edge of the zone period it landed in.
		start, end := t.ZoneBounds()
		if got.Before(w) {
			return end
		}
		return start
	}
	// Just after clocks fall back, the same reading also occurred one
	// offset change earlier; time.Date may have picked either.
	start, _ := t.ZoneBounds()
	if start.IsZero() {
		return t
	}
	_, off := t.Zone()
	_, prevOff := start.Add(-time.Second).Zone()
	if prevOff > off {
		earlier := t.Add(-time.Duration(prevOff-off) * time.Second)
		if earlier.Before(start) && civil(earlier).Equal(w) {
			return earlier
		}
	}
	return t
}
