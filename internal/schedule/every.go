package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Every fires at a fixed interval.
type Every struct {
	Interval time.Duration
}

// Next returns t plus the interval.
func (e Every) Next(t time.Time) time.Time { return t.Add(e.Interval) }

func (e Every) String() string { return "every " + FormatDuration(e.Interval) }

const day = 24 * time.Hour

// ParseDuration parses a Go duration such as "90s" or "1h30m", with an
// additional "d" unit for days that may lead the value ("2d", "1d12h").
func ParseDuration(s string) (time.Duration, error) {
	bad := fmt.Errorf("invalid duration %q (use e.g. 30s, 5m, 1h30m, 2d)", s)
	rest := s
	var days time.Duration
	if i := strings.IndexByte(rest, 'd'); i >= 0 {
		n, err := strconv.ParseFloat(rest[:i], 64)
		if err != nil || n < 0 {
			return 0, bad
		}
		days = time.Duration(n * float64(day))
		rest = rest[i+1:]
		if rest == "" {
			return days, nil
		}
		if rest[0] == '-' || rest[0] == '+' {
			return 0, bad
		}
	}
	d, err := time.ParseDuration(rest)
	if err != nil {
		return 0, bad
	}
	return days + d, nil
}

// FormatDuration formats d compactly in the syntax ParseDuration accepts:
// "1h30m" rather than "1h30m0s", "2d" rather than "48h0m0s".
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	if d >= day {
		fmt.Fprintf(&b, "%dd", d/day)
		d %= day
		if d == 0 {
			return b.String()
		}
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	b.WriteString(s)
	return b.String()
}
