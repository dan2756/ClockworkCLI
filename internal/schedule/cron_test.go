package schedule

import (
	"strings"
	"testing"
	"time"
)

func mustCron(t *testing.T, expr string) *Cron {
	t.Helper()
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("ParseCron(%q): %v", expr, err)
	}
	return c
}

func bitsOf(vals ...int) uint64 {
	var b uint64
	for _, v := range vals {
		b |= 1 << uint(v)
	}
	return b
}

func TestParseCronFields(t *testing.T) {
	c := mustCron(t, "*/15 9-17 1,15 jan-mar,Dec mon-fri")
	if want := bitsOf(0, 15, 30, 45); c.minute != want {
		t.Errorf("minute = %b, want %b", c.minute, want)
	}
	if want := bitsOf(9, 10, 11, 12, 13, 14, 15, 16, 17); c.hour != want {
		t.Errorf("hour = %b, want %b", c.hour, want)
	}
	if want := bitsOf(1, 15); c.dom != want {
		t.Errorf("dom = %b, want %b", c.dom, want)
	}
	if want := bitsOf(1, 2, 3, 12); c.month != want {
		t.Errorf("month = %b, want %b", c.month, want)
	}
	if want := bitsOf(1, 2, 3, 4, 5); c.dow != want {
		t.Errorf("dow = %b, want %b", c.dow, want)
	}
	if c.domAny || c.dowAny {
		t.Error("restricted day fields marked as any")
	}
}

func TestParseCronSteps(t *testing.T) {
	tests := []struct {
		expr  string
		field func(*Cron) uint64
		want  uint64
	}{
		{"5/20 * * * *", func(c *Cron) uint64 { return c.minute }, bitsOf(5, 25, 45)},
		{"0 9-17/4 * * *", func(c *Cron) uint64 { return c.hour }, bitsOf(9, 13, 17)},
		{"0 0 */10 * *", func(c *Cron) uint64 { return c.dom }, bitsOf(1, 11, 21, 31)},
		{"0 0 * */3 *", func(c *Cron) uint64 { return c.month }, bitsOf(1, 4, 7, 10)},
		{"0 0 * * */2", func(c *Cron) uint64 { return c.dow }, bitsOf(0, 2, 4, 6)},
		{"0 0 * * 7", func(c *Cron) uint64 { return c.dow }, bitsOf(0)},
		{"0 0 * * 5-7", func(c *Cron) uint64 { return c.dow }, bitsOf(0, 5, 6)},
		{"0 0 * * SAT,sun", func(c *Cron) uint64 { return c.dow }, bitsOf(0, 6)},
		{"1,2,3-4,10-20/5 * * * *", func(c *Cron) uint64 { return c.minute }, bitsOf(1, 2, 3, 4, 10, 15, 20)},
	}
	for _, tt := range tests {
		if got := tt.field(mustCron(t, tt.expr)); got != tt.want {
			t.Errorf("%q: got %b, want %b", tt.expr, got, tt.want)
		}
	}
}

func TestParseCronErrors(t *testing.T) {
	tests := []struct{ expr, want string }{
		{"", "want 5 fields"},
		{"* * * *", "got 4"},
		{"* * * * * *", "got 6"},
		{"60 * * * *", "minute: 60 out of range 0-59"},
		{"* 24 * * *", "hour: 24 out of range 0-23"},
		{"* * 0 * *", "day-of-month: 0 out of range 1-31"},
		{"* * * 13 *", "month: 13 out of range 1-12"},
		{"* * * * 8", "day-of-week: 8 out of range 0-7"},
		{"* * * foo *", `month: invalid value "foo"`},
		{"* * * * monday", `day-of-week: invalid value "monday"`},
		{"*/0 * * * *", `minute: invalid step "0"`},
		{"*/x * * * *", `minute: invalid step "x"`},
		{"5-1 * * * *", "runs backwards"},
		{"fri-mon * * * *", `minute: invalid value "fri"`},
		{"1,,2 * * * *", "empty item"},
		{"0 0 30 2 *", "never occurs"},
		{"0 0 31 apr,jun,sep,nov *", "never occurs"},
	}
	for _, tt := range tests {
		_, err := ParseCron(tt.expr)
		if err == nil {
			t.Errorf("ParseCron(%q) succeeded, want error containing %q", tt.expr, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ParseCron(%q) = %q, want it to contain %q", tt.expr, err, tt.want)
		}
	}
}

func TestParseCronOrSemanticsMakesDateValid(t *testing.T) {
	// Feb 30 never happens, but "or any Monday in February" does.
	mustCron(t, "0 0 30 2 mon")
}

func utc(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronNext(t *testing.T) {
	tests := []struct {
		expr, from, want string
	}{
		{"* * * * *", "2026-01-25 19:51", "2026-01-25 19:52"},
		{"*/15 * * * *", "2026-01-25 19:51", "2026-01-25 20:00"},
		{"*/15 9-17 * * 1-5", "2026-01-23 17:45", "2026-01-26 09:00"}, // Fri evening -> Mon
		{"*/15 9-17 * * 1-5", "2026-01-26 09:00", "2026-01-26 09:15"},
		{"0 0 1 * *", "2026-01-31 12:00", "2026-02-01 00:00"},
		{"0 0 31 * *", "2026-01-31 00:00", "2026-03-31 00:00"}, // skips February
		{"30 23 31 12 *", "2026-06-01 00:00", "2026-12-31 23:30"},
		{"0 0 1 1 *", "2026-12-31 23:59", "2027-01-01 00:00"},
		{"0 12 29 feb *", "2026-03-01 00:00", "2028-02-29 12:00"},
		{"0 0 29 2 *", "2096-03-01 00:00", "2104-02-29 00:00"},   // 2100 is not a leap year
		{"0 9 13 * fri", "2026-02-01 00:00", "2026-02-06 09:00"}, // dom OR dow: first Friday
		{"0 9 13 * fri", "2026-02-12 10:00", "2026-02-13 09:00"},
		{"0 9 */2 * mon", "2026-02-01 00:00", "2026-02-09 09:00"}, // dom starts with *: AND (odd-day Mondays)
		{"0 0 * * 0", "2026-01-25 00:00", "2026-02-01 00:00"},
		{"0 0 * * 7", "2026-01-25 00:00", "2026-02-01 00:00"},
		{"59 23 * * sat", "2026-01-31 23:59", "2026-02-07 23:59"},
		{"0 0 * jan mon", "2026-01-26 00:00", "2027-01-04 00:00"},
	}
	for _, tt := range tests {
		got := mustCron(t, tt.expr).Next(utc(tt.from))
		if want := utc(tt.want); !got.Equal(want) {
			t.Errorf("%q from %s: got %s, want %s", tt.expr, tt.from, got.Format("Mon 2006-01-02 15:04"), want.Format("Mon 2006-01-02 15:04"))
		}
	}
}

func TestCronNextSkipsSeconds(t *testing.T) {
	c := mustCron(t, "* * * * *")
	from := time.Date(2026, 1, 25, 19, 51, 59, 999, time.UTC)
	if got, want := c.Next(from), utc("2026-01-25 19:52"); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := c.Next(utc("2026-01-25 19:52")), utc("2026-01-25 19:53"); !got.Equal(want) {
		t.Errorf("Next of a fire time returned %v, want %v", got, want)
	}
}

func TestCronNextKeepsLocation(t *testing.T) {
	loc := time.FixedZone("UTC+5:30", 5*3600+1800)
	c := mustCron(t, "0 9 * * *")
	from := time.Date(2026, 1, 25, 10, 0, 0, 0, loc)
	got := c.Next(from)
	if want := time.Date(2026, 1, 26, 9, 0, 0, 0, loc); !got.Equal(want) || got.Location() != loc {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCronString(t *testing.T) {
	c := mustCron(t, "  */15   9-17 * *   1-5 ")
	if got := c.String(); got != `cron "*/15 9-17 * * 1-5"` {
		t.Errorf("String = %s", got)
	}
}

func TestParseCronNicknames(t *testing.T) {
	tests := []struct{ nick, same string }{
		{"@yearly", "0 0 1 1 *"},
		{"@annually", "0 0 1 1 *"},
		{"@monthly", "0 0 1 * *"},
		{"@weekly", "0 0 * * 0"},
		{"@daily", "0 0 * * *"},
		{"@midnight", "0 0 * * *"},
		{"@hourly", "0 * * * *"},
		{" @Daily ", "0 0 * * *"},
	}
	from := utc("2026-09-06 22:56")
	for _, tt := range tests {
		c := mustCron(t, tt.nick)
		want := mustCron(t, tt.same)
		for i, at := 0, from; i < 3; i++ {
			got, exp := c.Next(at), want.Next(at)
			if !got.Equal(exp) {
				t.Errorf("%s: fire %d = %v, want %v", tt.nick, i+1, got, exp)
			}
			at = got
		}
	}
	if got := mustCron(t, "@Daily").String(); got != `cron "@daily"` {
		t.Errorf("String = %s", got)
	}
	for _, bad := range []string{"@reboot", "@often", "@"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("ParseCron(%q) succeeded", bad)
		}
	}
}
