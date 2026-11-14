// Package cron reads the schedule of a cron job and tells when the schedule agrees with a time.
//
// A schedule has five fields: minute (0-59), hour (0-23), day of the month (1-31), month
// (1-12) and day of the week (0-7, where 0 and 7 are Sunday). A field is "*", a number, a
// range ("1-5"), a list ("1,15") or a step ("*/5", "10-40/10", "5/15"). The rule
// "cronSchedule" of schema/input-rules.json has the same form.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a schedule that Parse read. Each field is a set of bits: bit n is set when the
// field accepts the value n.
type Schedule struct {
	minute, hour, day, month, weekday uint64
	// anyDay and anyWeekday are true when the field is "*".
	anyDay, anyWeekday bool
}

// Parse reads a schedule.
func Parse(s string) (Schedule, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return Schedule{}, fmt.Errorf("the schedule %q must have 5 fields", s)
	}
	var out Schedule
	var err error
	if out.minute, err = field(f[0], 0, 59); err != nil {
		return Schedule{}, fmt.Errorf("minute: %w", err)
	}
	if out.hour, err = field(f[1], 0, 23); err != nil {
		return Schedule{}, fmt.Errorf("hour: %w", err)
	}
	if out.day, err = field(f[2], 1, 31); err != nil {
		return Schedule{}, fmt.Errorf("day of the month: %w", err)
	}
	if out.month, err = field(f[3], 1, 12); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	if out.weekday, err = field(f[4], 0, 7); err != nil {
		return Schedule{}, fmt.Errorf("day of the week: %w", err)
	}
	// 7 is Sunday, as 0.
	if out.weekday&(1<<7) != 0 {
		out.weekday |= 1
	}
	out.anyDay, out.anyWeekday = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return out, nil
}

// field reads one field with values from min to max.
func field(s string, min, max int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		spec, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			var err error
			if step, err = strconv.Atoi(stepText); err != nil || step < 1 {
				return 0, fmt.Errorf("the step %q is not valid", stepText)
			}
		}
		from, to := min, max
		if spec != "*" {
			a, b, isRange := strings.Cut(spec, "-")
			var err error
			if from, err = strconv.Atoi(a); err != nil {
				return 0, fmt.Errorf("%q is not a number", a)
			}
			to = from
			if isRange {
				if to, err = strconv.Atoi(b); err != nil {
					return 0, fmt.Errorf("%q is not a number", b)
				}
			} else if hasStep {
				// "5/15" starts at 5 and continues to the end.
				to = max
			}
		}
		if from < min || to > max || from > to {
			return 0, fmt.Errorf("%q is not from %d to %d", part, min, max)
		}
		for v := from; v <= to; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

// Matches reports whether the schedule agrees with the minute of t. The schedule uses the
// time zone of t.
//
// When the day of the month and the day of the week are both not "*", one of them must agree,
// as in cron.
func (s Schedule) Matches(t time.Time) bool {
	if s.minute&(1<<uint(t.Minute())) == 0 || s.hour&(1<<uint(t.Hour())) == 0 || s.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	day := s.day&(1<<uint(t.Day())) != 0
	weekday := s.weekday&(1<<uint(t.Weekday())) != 0
	if !s.anyDay && !s.anyWeekday {
		return day || weekday
	}
	return day && weekday
}
