package schedule

import (
	"slices"
	"testing"
)

func TestParseTimes(t *testing.T) {
	got, err := ParseTimes("13:30,09:00,9:00,23:59,00:00")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{0, 9 * 60, 13*60 + 30, 23*60 + 59}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"", "9", "24:00", "12:60", "12:5", "123:00", "-1:00", "+9:00", "12:00,", "noon", "12.30"} {
		if _, err := ParseTimes(bad); err == nil {
			t.Errorf("ParseTimes(%q) succeeded, want error", bad)
		}
	}
}

func TestParseDays(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"daily", "daily"},
		{"weekdays", "weekdays"},
		{"Weekends", "weekends"},
		{"mon,wed,fri", "mon,wed,fri"},
		{"fri,wed,mon", "mon,wed,fri"},
		{"mon-fri", "weekdays"},
		{"sat,sun", "weekends"},
		{"fri-mon", "mon,fri,sat,sun"},
		{"Monday,thursday", "mon,thu"},
		{"mon-sun", "daily"},
		{"tue-thu,sat", "tue,wed,thu,sat"},
	}
	for _, tt := range tests {
		d, err := ParseDays(tt.in)
		if err != nil {
			t.Errorf("ParseDays(%q): %v", tt.in, err)
			continue
		}
		if got := d.String(); got != tt.want {
			t.Errorf("ParseDays(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
	for _, bad := range []string{"", "mo", "funday", "mon,", "mon-", "mon--fri", "say"} {
		if _, err := ParseDays(bad); err == nil {
			t.Errorf("ParseDays(%q) succeeded, want error", bad)
		}
	}
}

func mustAt(t *testing.T, times, days string) *At {
	t.Helper()
	ts, err := ParseTimes(times)
	if err != nil {
		t.Fatal(err)
	}
	d := AllDays
	if days != "" {
		if d, err = ParseDays(days); err != nil {
			t.Fatal(err)
		}
	}
	return &At{Times: ts, Days: d}
}

func TestAtNext(t *testing.T) {
	// 2026-01-23 is a Friday.
	tests := []struct {
		times, days, from, want string
	}{
		{"09:00", "", "2026-01-23 08:59", "2026-01-23 09:00"},
		{"09:00", "", "2026-01-23 09:00", "2026-01-24 09:00"},
		{"09:00,13:30", "", "2026-01-23 09:00", "2026-01-23 13:30"},
		{"09:00,13:30", "", "2026-01-23 13:30", "2026-01-24 09:00"},
		{"09:00,13:30", "weekdays", "2026-01-23 14:00", "2026-01-26 09:00"},
		{"09:00,13:30", "weekdays", "2026-01-24 10:00", "2026-01-26 09:00"},
		{"10:00", "weekends", "2026-01-21 12:00", "2026-01-24 10:00"},
		{"10:00", "weekends", "2026-01-24 10:00", "2026-01-25 10:00"},
		{"10:00", "weekends", "2026-01-25 10:00", "2026-01-31 10:00"},
		{"07:15", "mon,wed,fri", "2026-01-26 07:15", "2026-01-28 07:15"},
		{"07:15", "mon,wed,fri", "2026-01-30 08:00", "2026-02-02 07:15"},
		{"23:59", "mon", "2026-01-26 23:59", "2026-02-02 23:59"},
		{"00:00", "", "2026-12-31 23:59", "2027-01-01 00:00"},
		{"12:00", "sun", "2028-02-28 13:00", "2028-03-05 12:00"},
	}
	for _, tt := range tests {
		a := mustAt(t, tt.times, tt.days)
		got := a.Next(utc(tt.from))
		if want := utc(tt.want); !got.Equal(want) {
			t.Errorf("%s from %s: got %s, want %s", a, tt.from,
				got.Format("Mon 2006-01-02 15:04"), want.Format("Mon 2006-01-02 15:04"))
		}
	}
}

func TestAtString(t *testing.T) {
	if got := mustAt(t, "13:30,9:00", "weekdays").String(); got != "at 09:00,13:30 weekdays" {
		t.Errorf("got %q", got)
	}
	if got := mustAt(t, "09:00", "").String(); got != "at 09:00" {
		t.Errorf("got %q", got)
	}
	if got := mustAt(t, "09:00", "mon-sun").String(); got != "at 09:00" {
		t.Errorf("got %q", got)
	}
}
