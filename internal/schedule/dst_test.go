package schedule

import (
	"testing"
	"time"

	// Embed the zone database so these tests also run on systems without
	// one installed (minimal containers, some CI images).
	_ "time/tzdata"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("time zone %s unavailable: %v", name, err)
	}
	return loc
}

func at(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04 MST", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

// sequence returns the next n fire times of s after from.
func sequence(s Schedule, from time.Time, n int) []time.Time {
	var out []time.Time
	for range n {
		from = s.Next(from)
		out = append(out, from)
	}
	return out
}

func checkSequence(t *testing.T, s Schedule, from time.Time, want ...time.Time) {
	t.Helper()
	got := sequence(s, from, len(want))
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("%s from %s: fire %d = %s, want %s", s, from.Format("Jan 2 15:04 MST"), i+1,
				got[i].Format("Mon Jan 2 15:04 MST"), want[i].Format("Mon Jan 2 15:04 MST"))
		}
	}
}

// In America/New_York, 2026-03-08 02:00 EST jumps to 03:00 EDT.
func TestCronSpringForward(t *testing.T) {
	ny := zone(t, "America/New_York")

	// 02:30 does not exist that day: run at the jump instead of skipping.
	checkSequence(t, mustCron(t, "30 2 * * *"), at(ny, "2026-03-07 12:00 EST"),
		at(ny, "2026-03-08 03:00 EDT"),
		at(ny, "2026-03-09 02:30 EDT"),
	)
	// Every 30 minutes stays 30 real minutes apart across the jump.
	checkSequence(t, mustCron(t, "*/30 * * * *"), at(ny, "2026-03-08 01:00 EST"),
		at(ny, "2026-03-08 01:30 EST"),
		at(ny, "2026-03-08 03:00 EDT"),
		at(ny, "2026-03-08 03:30 EDT"),
	)
	checkSequence(t, mustCron(t, "0 * * * *"), at(ny, "2026-03-08 00:30 EST"),
		at(ny, "2026-03-08 01:00 EST"),
		at(ny, "2026-03-08 03:00 EDT"),
		at(ny, "2026-03-08 04:00 EDT"),
	)
	// Wall clock times stay put: 09:00 is 09:00 on both sides.
	nine := mustCron(t, "0 9 * * *")
	first := nine.Next(at(ny, "2026-03-07 00:00 EST"))
	second := nine.Next(first)
	if d := second.Sub(first); d != 23*time.Hour {
		t.Errorf("09:00 to 09:00 across spring forward = %v, want 23h", d)
	}
}

// In America/New_York, 2026-11-01 02:00 EDT falls back to 01:00 EST.
func TestCronFallBack(t *testing.T) {
	ny := zone(t, "America/New_York")

	// 01:30 happens twice; the job runs once.
	checkSequence(t, mustCron(t, "30 1 * * *"), at(ny, "2026-10-31 12:00 EDT"),
		at(ny, "2026-11-01 01:30 EDT"),
		at(ny, "2026-11-02 01:30 EST"),
	)
	checkSequence(t, mustCron(t, "0 * * * *"), at(ny, "2026-11-01 00:30 EDT"),
		at(ny, "2026-11-01 01:00 EDT"),
		at(ny, "2026-11-01 02:00 EST"),
		at(ny, "2026-11-01 03:00 EST"),
	)
	// Starting during the repeated hour: the readings left in it already
	// happened the first time round.
	checkSequence(t, mustCron(t, "*/15 * * * *"), at(ny, "2026-11-01 01:10 EST"),
		at(ny, "2026-11-01 02:00 EST"),
		at(ny, "2026-11-01 02:15 EST"),
	)
}

func TestCronMonthBoundaryInZone(t *testing.T) {
	ny := zone(t, "America/New_York")
	checkSequence(t, mustCron(t, "0 0 1 * *"), at(ny, "2026-10-31 23:59 EDT"),
		at(ny, "2026-11-01 00:00 EDT"),
		at(ny, "2026-12-01 00:00 EST"),
	)
	checkSequence(t, mustCron(t, "0 12 31 * *"), at(ny, "2026-01-31 12:00 EST"),
		at(ny, "2026-03-31 12:00 EDT"),
		at(ny, "2026-05-31 12:00 EDT"),
	)
}

// Zones east of UTC: in Europe/Berlin, 2026-10-25 03:00 CEST falls back to
// 02:00 CET, and time.Date resolves the repeated hour the other way round.
func TestCronFallBackEastOfUTC(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	checkSequence(t, mustCron(t, "30 2 * * *"), at(berlin, "2026-10-24 12:00 CEST"),
		at(berlin, "2026-10-25 02:30 CEST"),
		at(berlin, "2026-10-26 02:30 CET"),
	)
	// And its spring gap, 2026-03-29 02:00 CET -> 03:00 CEST.
	checkSequence(t, mustCron(t, "30 2 * * *"), at(berlin, "2026-03-28 12:00 CET"),
		at(berlin, "2026-03-29 03:00 CEST"),
		at(berlin, "2026-03-30 02:30 CEST"),
	)
}

func TestAtAcrossDST(t *testing.T) {
	ny := zone(t, "America/New_York")

	checkSequence(t, mustAt(t, "02:30", ""), at(ny, "2026-03-07 12:00 EST"),
		at(ny, "2026-03-08 03:00 EDT"),
		at(ny, "2026-03-09 02:30 EDT"),
	)
	checkSequence(t, mustAt(t, "01:30", ""), at(ny, "2026-10-31 12:00 EDT"),
		at(ny, "2026-11-01 01:30 EDT"),
		at(ny, "2026-11-02 01:30 EST"),
	)
	// Weekday filter across the spring jump: Friday -> Monday.
	checkSequence(t, mustAt(t, "09:00,13:30", "weekdays"), at(ny, "2026-03-06 14:00 EST"),
		at(ny, "2026-03-09 09:00 EDT"),
		at(ny, "2026-03-09 13:30 EDT"),
	)
	// Weekends only, across the autumn change.
	checkSequence(t, mustAt(t, "01:30", "weekends"), at(ny, "2026-10-30 12:00 EDT"),
		at(ny, "2026-10-31 01:30 EDT"),
		at(ny, "2026-11-01 01:30 EDT"),
		at(ny, "2026-11-07 01:30 EST"),
	)
}

func TestInZoneOutsideTransitions(t *testing.T) {
	ny := zone(t, "America/New_York")
	w := time.Date(2026, 6, 15, 10, 47, 0, 0, time.UTC)
	if got, want := inZone(w, ny), at(ny, "2026-06-15 10:47 EDT"); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got := inZone(w, time.UTC); !got.Equal(w) {
		t.Errorf("UTC: got %v, want %v", got, w)
	}
}
