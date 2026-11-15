package cron

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMatches(t *testing.T) {
	// 2026-11-14 is a Saturday.
	cases := []struct {
		schedule, time string
		want           bool
	}{
		{"* * * * *", "2026-11-14 10:07", true},
		{"*/5 * * * *", "2026-11-14 10:05", true},
		{"*/5 * * * *", "2026-11-14 10:07", false},
		{"0 3 * * *", "2026-11-14 03:00", true},
		{"0 3 * * *", "2026-11-14 04:00", false},
		{"15,45 8-18 * * 1-5", "2026-11-13 08:45", true},
		{"15,45 8-18 * * 1-5", "2026-11-14 08:45", false},
		{"0 0 1 * *", "2026-12-01 00:00", true},
		{"0 0 1 * *", "2026-12-02 00:00", false},
		{"0 0 * * 7", "2026-11-15 00:00", true},
		{"0 0 * * 0", "2026-11-15 00:00", true},
		{"0 0 * * 6", "2026-11-14 00:00", true},
		// Day of the month or day of the week.
		{"0 0 1 * 6", "2026-11-14 00:00", true},
		{"0 0 1 * 6", "2026-12-01 00:00", true},
		{"0 0 1 * 6", "2026-11-16 00:00", false},
		{"5/20 * * * *", "2026-11-14 10:25", true},
		{"5/20 * * * *", "2026-11-14 10:20", false},
		{"10-40/10 * * * *", "2026-11-14 10:30", true},
		{"10-40/10 * * * *", "2026-11-14 10:50", false},
		{"0 12 * 6-8 *", "2026-07-01 12:00", true},
		{"0 12 * 6-8 *", "2026-11-14 12:00", false},
	}
	for _, c := range cases {
		s, err := Parse(c.schedule)
		if err != nil {
			t.Fatalf("%q: %v", c.schedule, err)
		}
		if got := s.Matches(at(c.time)); got != c.want {
			t.Errorf("%q at %s = %v, want %v", c.schedule, c.time, got, c.want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, s := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "5-1 * * * *", "a * * * *", "1,,2 * * * *"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) gave no error", s)
		}
	}
}
