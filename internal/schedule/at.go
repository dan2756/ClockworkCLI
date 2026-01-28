package schedule

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Days is a set of weekdays.
type Days uint8

// Common sets of days.
const (
	Weekdays Days = 1<<time.Monday | 1<<time.Tuesday | 1<<time.Wednesday | 1<<time.Thursday | 1<<time.Friday
	Weekends Days = 1<<time.Saturday | 1<<time.Sunday
	AllDays       = Weekdays | Weekends
)

var dayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Has reports whether wd is in the set.
func (d Days) Has(wd time.Weekday) bool { return d&(1<<wd) != 0 }

// String returns "daily", "weekdays", "weekends" or a list such as
// "mon,wed,fri".
func (d Days) String() string {
	switch d {
	case AllDays:
		return "daily"
	case Weekdays:
		return "weekdays"
	case Weekends:
		return "weekends"
	}
	var names []string
	for i := range 7 {
		wd := time.Weekday((i + 1) % 7) // list Monday first
		if d.Has(wd) {
			names = append(names, dayNames[wd])
		}
	}
	return strings.Join(names, ",")
}

// ParseDays parses "daily", "weekdays", "weekends", or a comma-separated
// list of day names and ranges such as "mon,wed,fri" or "mon-thu,sat".
// Ranges may wrap around the weekend ("fri-mon").
func ParseDays(s string) (Days, error) {
	switch strings.ToLower(s) {
	case "daily":
		return AllDays, nil
	case "weekdays":
		return Weekdays, nil
	case "weekends":
		return Weekends, nil
	}
	var d Days
	for _, part := range strings.Split(s, ",") {
		a, b, isRange := strings.Cut(part, "-")
		lo, ok := dayIndex(a)
		if !ok {
			return 0, fmt.Errorf("invalid day %q", a)
		}
		hi := lo
		if isRange {
			if hi, ok = dayIndex(b); !ok {
				return 0, fmt.Errorf("invalid day %q", b)
			}
		}
		for i := lo; ; i = (i + 1) % 7 {
			d |= 1 << i
			if i == hi {
				break
			}
		}
	}
	return d, nil
}

func dayIndex(s string) (int, bool) {
	s = strings.ToLower(s)
	if len(s) < 3 {
		return 0, false
	}
	for i, name := range dayNames {
		if s == name {
			return i, true
		}
		full := time.Weekday(i).String()
		if s == strings.ToLower(full) {
			return i, true
		}
	}
	return 0, false
}

// At fires at fixed wall clock times on selected days.
type At struct {
	Times []int // minutes after midnight, sorted and unique
	Days  Days
}

// ParseTimes parses a comma-separated list of HH:MM times and returns
// them as sorted minutes after midnight.
func ParseTimes(s string) ([]int, error) {
	var out []int
	for _, part := range strings.Split(s, ",") {
		h, m, ok := strings.Cut(part, ":")
		hh, okH := atoi(h)
		mm, okM := atoi(m)
		if !ok || !okH || !okM || len(h) > 2 || len(m) != 2 || hh > 23 || mm > 59 {
			return nil, fmt.Errorf("invalid time %q (want HH:MM between 00:00 and 23:59)", part)
		}
		out = append(out, hh*60+mm)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// atoi parses a short unsigned decimal number.
func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// Next returns the first listed time on an allowed day strictly after t.
func (a *At) Next(t time.Time) time.Time {
	if len(a.Times) == 0 || a.Days == 0 {
		return time.Time{}
	}
	loc := t.Location()
	y, m, d := t.Date()
	for i := range 8 {
		date := time.Date(y, m, d+i, 0, 0, 0, 0, time.UTC)
		if !a.Days.Has(date.Weekday()) {
			continue
		}
		for _, min := range a.Times {
			if at := inZone(date.Add(time.Duration(min)*time.Minute), loc); at.After(t) {
				return at
			}
		}
	}
	return time.Time{}
}

func (a *At) String() string {
	times := make([]string, len(a.Times))
	for i, min := range a.Times {
		times[i] = fmt.Sprintf("%02d:%02d", min/60, min%60)
	}
	s := "at " + strings.Join(times, ",")
	if a.Days != AllDays {
		s += " " + a.Days.String()
	}
	return s
}
