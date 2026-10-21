package schedule

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"10s", 10 * time.Second},
		{"5m", 5 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"250ms", 250 * time.Millisecond},
		{"1d", 24 * time.Hour},
		{"2d", 48 * time.Hour},
		{"1d12h", 36 * time.Hour},
		{"1.5d", 36 * time.Hour},
		{"0s", 0},
		{"100000d", 100000 * 24 * time.Hour},
		// Largest whole number of days that fits in a Duration.
		{"106751d", 106751 * 24 * time.Hour},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.in)
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseDurationErrors(t *testing.T) {
	for _, in := range []string{
		"", "5", "5x", "d", "-1d", "1d-5h", "1h1d", "five minutes",
		// These used to come back negative instead of failing.
		"106752d", "1e6d", "NaNd", "Infd", "+Infd", "106751d24h", "106751d23h48m",
	} {
		if d, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = %v, want error", in, d)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "10s"},
		{5 * time.Minute, "5m"},
		{90 * time.Minute, "1h30m"},
		{2 * time.Hour, "2h"},
		{24 * time.Hour, "1d"},
		{36 * time.Hour, "1d12h"},
		{1500 * time.Millisecond, "1.5s"},
		{0, "0s"},
	}
	for _, tt := range tests {
		if got := FormatDuration(tt.in); got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.in, got, tt.want)
		}
		if tt.in > 0 {
			back, err := ParseDuration(FormatDuration(tt.in))
			if err != nil || back != tt.in {
				t.Errorf("round trip of %v: got %v, %v", tt.in, back, err)
			}
		}
	}
}

func TestEveryNext(t *testing.T) {
	start := time.Date(2026, 1, 25, 19, 51, 18, 0, time.UTC)
	e := Every{Interval: 90 * time.Minute}
	if got, want := e.Next(start), start.Add(90*time.Minute); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	if got := e.String(); got != "every 1h30m" {
		t.Errorf("String = %q", got)
	}
}
