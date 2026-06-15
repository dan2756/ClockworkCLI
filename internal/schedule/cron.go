package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a standard five-field cron expression:
//
//	minute hour day-of-month month day-of-week
//
// Each field accepts *, single values, ranges (1-5), lists (1,15,30) and
// steps (*/15, 9-17/2, 5/10). Months and weekdays may be given by their
// three-letter English names (jan, mon), and both 0 and 7 mean Sunday.
//
// As in classic cron, when both day-of-month and day-of-week are
// restricted (neither starts with *), a day matches if either field
// matches.
type Cron struct {
	expr   string
	minute uint64
	hour   uint64
	dom    uint64
	month  uint64
	dow    uint64
	domAny bool // day-of-month field starts with *
	dowAny bool // day-of-week field starts with *
}

type cronField struct {
	name     string
	min, max int
	names    []string // names[i] is the value min+i
}

var cronFields = [5]cronField{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day-of-month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{
		"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	{name: "day-of-week", min: 0, max: 7, names: []string{
		"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

// ParseCron parses a five-field cron expression.
func ParseCron(expr string) (*Cron, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("want 5 fields (minute hour day-of-month month day-of-week), got %d", len(fields))
	}
	var bits [5]uint64
	for i, f := range cronFields {
		b, err := f.parse(fields[i])
		if err != nil {
			return nil, err
		}
		bits[i] = b
	}
	c := &Cron{
		expr:   strings.Join(fields, " "),
		minute: bits[0],
		hour:   bits[1],
		dom:    bits[2],
		month:  bits[3],
		dow:    bits[4],
		domAny: strings.HasPrefix(fields[2], "*"),
		dowAny: strings.HasPrefix(fields[4], "*"),
	}
	if c.dow&(1<<7) != 0 {
		c.dow = c.dow&^(1<<7) | 1 // 7 is another name for Sunday
	}
	if !c.possible() {
		return nil, fmt.Errorf("day-of-month %s never occurs in month %s", fields[2], fields[3])
	}
	return c, nil
}

func (f cronField) parse(s string) (uint64, error) {
	var bits uint64
	for _, item := range strings.Split(s, ",") {
		if item == "" {
			return 0, fmt.Errorf("%s: empty item in list %q", f.name, s)
		}
		rng, stepText, hasStep := strings.Cut(item, "/")
		lo, hi := f.min, f.max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = f.value(a); err != nil {
				return 0, err
			}
			switch {
			case isRange:
				if hi, err = f.value(b); err != nil {
					return 0, err
				}
				if lo > hi {
					return 0, fmt.Errorf("%s: range %q runs backwards", f.name, rng)
				}
			case !hasStep:
				hi = lo
			}
		}
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%s: invalid step %q", f.name, stepText)
			}
			step = n
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func (f cronField) value(s string) (int, error) {
	for i, name := range f.names {
		if strings.EqualFold(s, name) {
			return f.min + i, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q", f.name, s)
	}
	if n < f.min || n > f.max {
		return 0, fmt.Errorf("%s: %d out of range %d-%d", f.name, n, f.min, f.max)
	}
	return n, nil
}

// daysIn is the longest each month can be.
var daysIn = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// possible reports whether the expression matches at least one real date.
// Only day-of-month can rule out every date ("30 feb"): an expression that
// also restricts the weekday matches every week of the year.
func (c *Cron) possible() bool {
	if !c.domAny && !c.dowAny {
		return true
	}
	for m := 1; m <= 12; m++ {
		if c.month&(1<<uint(m)) == 0 {
			continue
		}
		for d := 1; d <= daysIn[m]; d++ {
			if c.dom&(1<<uint(d)) != 0 {
				return true
			}
		}
	}
	return false
}

func (c *Cron) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<uint(t.Day())) != 0
	dow := c.dow&(1<<uint(t.Weekday())) != 0
	if c.domAny || c.dowAny {
		return dom && dow
	}
	return dom || dow
}

// Next returns the first matching minute strictly after t, in t's
// location.
func (c *Cron) Next(t time.Time) time.Time {
	loc := t.Location()
	// Search on the wall clock, represented in UTC so that stepping by
	// days and hours is plain arithmetic.
	w := civil(t).Truncate(time.Minute).Add(time.Minute)
	// A possible expression always fires within 8 years (Feb 29 skips 2100).
	last := w.Year() + 9
	for w.Year() <= last {
		y, m, d := w.Date()
		switch {
		case c.month&(1<<uint(m)) == 0:
			w = time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.dayMatches(w):
			w = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
		case c.hour&(1<<uint(w.Hour())) == 0:
			w = w.Truncate(time.Hour).Add(time.Hour)
		case c.minute&(1<<uint(w.Minute())) == 0:
			w = w.Add(time.Minute)
		default:
			if at := inZone(w, loc); at.After(t) {
				return at
			}
			w = w.Add(time.Minute)
		}
	}
	return time.Time{}
}

// Expr returns the expression in canonical form.
func (c *Cron) Expr() string { return c.expr }

func (c *Cron) String() string { return `cron "` + c.expr + `"` }
