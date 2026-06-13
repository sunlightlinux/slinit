package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CalendarSpec is a parsed systemd-style OnCalendar expression. A nil
// slice on any component means "any value". Slices are kept sorted
// ascending so NextAfter can find the next allowed value with a single
// binary search.
//
// Supported subset:
//
//	Aliases:        minutely, hourly, daily/midnight, weekly, monthly,
//	                yearly/annually
//	Weekday lists:  Mon, Mon,Wed,Fri, Mon..Fri
//	Date pattern:   *-*-* with optional month/day fixed values
//	Time pattern:   HH:MM, HH:MM:SS, *:MM, *:MM:SS, HH:*, *:0/15 (step)
//
// Out of scope: year specification, negative day-of-month, week-of-year.
type CalendarSpec struct {
	weekdays []time.Weekday // nil = any
	months   []time.Month   // nil = any
	days     []int          // 1..31, nil = any
	hours    []int          // 0..23, nil = any
	minutes  []int          // 0..59, nil = any
	seconds  []int          // 0..59, nil = any
}

// ParseCalendar decodes a systemd-style OnCalendar expression.
func ParseCalendar(s string) (*CalendarSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty calendar expression")
	}
	if spec, ok := calendarAlias(s); ok {
		return spec, nil
	}

	spec := &CalendarSpec{}
	parts := strings.Fields(s)

	// Optional weekday-list head: bare word that looks like a day name.
	if len(parts) >= 2 && isWeekdayPart(parts[0]) {
		wds, err := parseWeekdays(parts[0])
		if err != nil {
			return nil, err
		}
		spec.weekdays = wds
		parts = parts[1:]
	}

	// Optional date head: contains '-'.
	if len(parts) >= 2 && strings.Contains(parts[0], "-") {
		ms, ds, err := parseDate(parts[0])
		if err != nil {
			return nil, err
		}
		spec.months = ms
		spec.days = ds
		parts = parts[1:]
	}

	if len(parts) != 1 {
		return nil, fmt.Errorf("calendar: expected exactly one time field, got %d", len(parts))
	}
	hs, mn, sc, err := parseTime(parts[0])
	if err != nil {
		return nil, err
	}
	spec.hours = hs
	spec.minutes = mn
	spec.seconds = sc
	return spec, nil
}

// NextAfter returns the next time at or after `after` that matches the
// spec. Searches up to two years ahead and returns the zero time if
// no match is found (e.g. impossible spec like Feb 30).
func (c *CalendarSpec) NextAfter(after time.Time) time.Time {
	// Start one second after `after` to avoid returning `after` itself.
	t := after.Add(time.Second).Truncate(time.Second)

	deadline := t.Add(2 * 365 * 24 * time.Hour)

	for t.Before(deadline) {
		// Date components first: weekday, month, day-of-month.
		if !inAny(c.weekdays, weekdayContains, t.Weekday()) {
			// Advance to next day, reset time.
			t = time.Date(t.Year(), t.Month(), t.Day()+1,
				0, 0, 0, 0, t.Location())
			continue
		}
		if !monthAllowed(c.months, t.Month()) {
			// Advance to next month, reset day/time.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !containsInt(c.days, t.Day()) {
			// Advance to next day, reset time.
			t = time.Date(t.Year(), t.Month(), t.Day()+1,
				0, 0, 0, 0, t.Location())
			continue
		}
		// Time components.
		if !containsInt(c.hours, t.Hour()) {
			nextHour, ok := nextAllowed(c.hours, t.Hour()+1, 23)
			if !ok {
				// No more allowed hours today; jump to next day.
				t = time.Date(t.Year(), t.Month(), t.Day()+1,
					0, 0, 0, 0, t.Location())
				continue
			}
			t = time.Date(t.Year(), t.Month(), t.Day(),
				nextHour, 0, 0, 0, t.Location())
			continue
		}
		if !containsInt(c.minutes, t.Minute()) {
			nextMin, ok := nextAllowed(c.minutes, t.Minute()+1, 59)
			if !ok {
				t = time.Date(t.Year(), t.Month(), t.Day(),
					t.Hour()+1, 0, 0, 0, t.Location())
				continue
			}
			t = time.Date(t.Year(), t.Month(), t.Day(),
				t.Hour(), nextMin, 0, 0, t.Location())
			continue
		}
		if !containsInt(c.seconds, t.Second()) {
			nextSec, ok := nextAllowed(c.seconds, t.Second()+1, 59)
			if !ok {
				t = time.Date(t.Year(), t.Month(), t.Day(),
					t.Hour(), t.Minute()+1, 0, 0, t.Location())
				continue
			}
			t = time.Date(t.Year(), t.Month(), t.Day(),
				t.Hour(), t.Minute(), nextSec, 0, t.Location())
			continue
		}
		return t
	}
	return time.Time{}
}

// --- helpers ---

func calendarAlias(s string) (*CalendarSpec, bool) {
	switch strings.ToLower(s) {
	case "minutely":
		return &CalendarSpec{seconds: []int{0}}, true
	case "hourly":
		return &CalendarSpec{minutes: []int{0}, seconds: []int{0}}, true
	case "daily", "midnight":
		return &CalendarSpec{hours: []int{0}, minutes: []int{0}, seconds: []int{0}}, true
	case "weekly":
		return &CalendarSpec{
			weekdays: []time.Weekday{time.Monday},
			hours:    []int{0}, minutes: []int{0}, seconds: []int{0},
		}, true
	case "monthly":
		return &CalendarSpec{
			days:  []int{1},
			hours: []int{0}, minutes: []int{0}, seconds: []int{0},
		}, true
	case "yearly", "annually":
		return &CalendarSpec{
			months: []time.Month{time.January},
			days:   []int{1},
			hours:  []int{0}, minutes: []int{0}, seconds: []int{0},
		}, true
	}
	return nil, false
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
	"sat": time.Saturday,
}

func isWeekdayPart(s string) bool {
	first := s
	if i := strings.IndexAny(s, ",."); i >= 0 {
		first = s[:i]
	}
	_, ok := weekdayNames[strings.ToLower(first)]
	return ok
}

func parseWeekdays(s string) ([]time.Weekday, error) {
	out := map[time.Weekday]struct{}{}
	for _, item := range strings.Split(s, ",") {
		if strings.Contains(item, "..") {
			rng := strings.SplitN(item, "..", 2)
			a, ok1 := weekdayNames[strings.ToLower(rng[0])]
			b, ok2 := weekdayNames[strings.ToLower(rng[1])]
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("unknown weekday in %q", item)
			}
			d := a
			for {
				out[d] = struct{}{}
				if d == b {
					break
				}
				d = (d + 1) % 7
			}
		} else {
			d, ok := weekdayNames[strings.ToLower(item)]
			if !ok {
				return nil, fmt.Errorf("unknown weekday %q", item)
			}
			out[d] = struct{}{}
		}
	}
	wds := make([]time.Weekday, 0, len(out))
	for d := range out {
		wds = append(wds, d)
	}
	sort.Slice(wds, func(i, j int) bool { return wds[i] < wds[j] })
	return wds, nil
}

// parseDate decodes "*-*-DD", "*-MM-DD", "YYYY-*-*", etc. The year
// component is currently ignored (returned as not-constraining months
// or days separately).
func parseDate(s string) (months []time.Month, days []int, err error) {
	fields := strings.Split(s, "-")
	if len(fields) != 3 {
		return nil, nil, fmt.Errorf("calendar date %q: expected YYYY-MM-DD", s)
	}
	// fields[0] = year (ignored), fields[1] = month, fields[2] = day
	if fields[1] != "*" {
		m, err := strconv.Atoi(fields[1])
		if err != nil || m < 1 || m > 12 {
			return nil, nil, fmt.Errorf("calendar month %q: out of range", fields[1])
		}
		months = []time.Month{time.Month(m)}
	}
	if fields[2] != "*" {
		d, err := strconv.Atoi(fields[2])
		if err != nil || d < 1 || d > 31 {
			return nil, nil, fmt.Errorf("calendar day %q: out of range", fields[2])
		}
		days = []int{d}
	}
	return months, days, nil
}

// parseTime decodes "HH:MM[:SS]" with '*' wildcards and "/step" suffixes.
func parseTime(s string) (hours, minutes, seconds []int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, nil, nil, fmt.Errorf("calendar time %q: expected HH:MM or HH:MM:SS", s)
	}
	hours, err = parseTimeField(parts[0], 0, 23)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hours: %w", err)
	}
	minutes, err = parseTimeField(parts[1], 0, 59)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("minutes: %w", err)
	}
	if len(parts) == 3 {
		seconds, err = parseTimeField(parts[2], 0, 59)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("seconds: %w", err)
		}
	} else {
		seconds = []int{0}
	}
	return hours, minutes, seconds, nil
}

// parseTimeField decodes one HH/MM/SS component:
//
//	"*"        → nil (any)
//	"*/15"     → step 15 starting at 0
//	"0/15"     → step 15 starting at 0
//	"5/10"     → step 10 starting at 5
//	"42"       → single value
//	"5,10,15"  → set
func parseTimeField(s string, lo, hi int) ([]int, error) {
	if s == "*" {
		return nil, nil
	}
	if strings.Contains(s, "/") {
		seg := strings.SplitN(s, "/", 2)
		start := 0
		if seg[0] != "*" {
			v, err := strconv.Atoi(seg[0])
			if err != nil {
				return nil, fmt.Errorf("invalid start %q", seg[0])
			}
			start = v
		}
		step, err := strconv.Atoi(seg[1])
		if err != nil || step <= 0 {
			return nil, fmt.Errorf("invalid step %q", seg[1])
		}
		var out []int
		for v := start; v <= hi; v += step {
			if v >= lo {
				out = append(out, v)
			}
		}
		return out, nil
	}
	if strings.Contains(s, ",") {
		var out []int
		for _, item := range strings.Split(s, ",") {
			v, err := strconv.Atoi(item)
			if err != nil || v < lo || v > hi {
				return nil, fmt.Errorf("invalid value %q", item)
			}
			out = append(out, v)
		}
		sort.Ints(out)
		return out, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return nil, fmt.Errorf("invalid value %q", s)
	}
	return []int{v}, nil
}

func containsInt(slice []int, v int) bool {
	if slice == nil {
		return true
	}
	for _, x := range slice {
		if x == v {
			return true
		}
	}
	return false
}

func monthAllowed(slice []time.Month, v time.Month) bool {
	if slice == nil {
		return true
	}
	for _, x := range slice {
		if x == v {
			return true
		}
	}
	return false
}

func weekdayContains(slice []time.Weekday, v time.Weekday) bool {
	for _, x := range slice {
		if x == v {
			return true
		}
	}
	return false
}

// inAny applies the contains predicate, treating nil/empty slice as
// "matches anything". Generic in spirit but written long-form to avoid
// pulling in type-parameter helpers.
func inAny[T any](slice []T, contains func([]T, T) bool, v T) bool {
	if len(slice) == 0 {
		return true
	}
	return contains(slice, v)
}

// nextAllowed returns the smallest value in `slice` that is >= `from`
// and <= `max`. Returns (0, false) when no such value exists.
func nextAllowed(slice []int, from, max int) (int, bool) {
	if slice == nil {
		if from > max {
			return 0, false
		}
		return from, true
	}
	for _, v := range slice {
		if v >= from && v <= max {
			return v, true
		}
	}
	return 0, false
}
