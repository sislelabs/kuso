// Package cronsched computes fire times for the standard 5-field cron
// schedules KusoCron accepts (crons.validateSchedule), with the same
// semantics as the robfig/cron parser the kube CronJob controller uses:
// month/day-of-week names, the @hourly..@yearly macros, and OR-matching
// of day-of-month and day-of-week when both are restricted.
package cronsched

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is returned for a schedule this package can't parse.
var ErrInvalid = errors.New("invalid cron schedule")

// Schedule is a parsed 5-field cron expression.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var names = [5]map[string]int{
	3: {"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12},
	4: {"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6},
}

var bounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// Parse parses a 5-field expression or one of the @ macros.
func Parse(spec string) (*Schedule, error) {
	spec = strings.TrimSpace(spec)
	if m, ok := macros[strings.ToLower(spec)]; ok {
		spec = m
	}
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: %q must have 5 fields", ErrInvalid, spec)
	}
	var bits [5]uint64
	var star [5]bool
	for i, f := range fields {
		b, s, err := parseField(strings.ToLower(f), i)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %v", ErrInvalid, spec, err)
		}
		bits[i], star[i] = b, s
	}
	// 7 is Sunday too.
	if bits[4]&(1<<7) != 0 {
		bits[4] |= 1
	}
	return &Schedule{minute: bits[0], hour: bits[1], dom: bits[2], month: bits[3], dow: bits[4], domStar: star[2], dowStar: star[4]}, nil
}

func parseField(f string, idx int) (uint64, bool, error) {
	lo, hi := bounds[idx][0], bounds[idx][1]
	var out uint64
	star := false
	for _, part := range strings.Split(f, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, false, fmt.Errorf("bad step in %q", part)
			}
			step = n
		}
		var start, end int
		switch {
		case rng == "*" || rng == "?":
			start, end = lo, hi
			if step == 1 {
				star = true
			}
		default:
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if start, err = value(a, idx); err != nil {
				return 0, false, err
			}
			end = start
			if isRange {
				if end, err = value(b, idx); err != nil {
					return 0, false, err
				}
			} else if hasStep {
				end = hi
			}
		}
		if start < lo || end > hi || start > end {
			return 0, false, fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := start; v <= end; v += step {
			out |= 1 << uint(v)
		}
	}
	return out, star, nil
}

func value(s string, idx int) (int, error) {
	if n, ok := names[idx][s]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return n, nil
}

// searchLimit bounds Next: a schedule with no fire time in five years
// (e.g. "0 0 30 2 *") never fires.
const searchLimit = 5 * 366 * 24 * time.Hour

// Next returns the first fire time strictly after t, in t's location,
// or the zero time when the schedule never fires.
func (s *Schedule) Next(t time.Time) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(searchLimit)
	for t.Before(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func (s *Schedule) dayMatches(t time.Time) bool {
	domMatch := s.dom&(1<<uint(t.Day())) != 0
	dowMatch := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domStar || s.dowStar {
		return domMatch && dowMatch
	}
	return domMatch || dowMatch
}

// Interval is the gap between the next two fire times after t: the
// period a run has before it starts holding up the next one. Zero when
// the schedule fires fewer than twice in the search window.
func (s *Schedule) Interval(t time.Time) time.Duration {
	a := s.Next(t)
	if a.IsZero() {
		return 0
	}
	b := s.Next(a)
	if b.IsZero() {
		return 0
	}
	return b.Sub(a)
}
