package service

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	// A calendar expression may name a timezone, and an init system has to
	// resolve it with no /usr/share/zoneinfo: PID 1 parses service
	// descriptions before the filesystems holding tzdata are mounted, and a
	// minimal rootfs may not ship the database at all. Embedding it trades
	// binary size for a zone name that means the same thing everywhere.
	_ "time/tzdata"
)

// CalendarSpec is a parsed systemd-style OnCalendar expression. A nil
// slice on any component means "any value". Slices are kept sorted
// ascending and deduplicated so the search can walk them in order.
//
// Supported:
//
//	Aliases:        minutely, hourly, daily/midnight, weekly, monthly,
//	                quarterly, semiannually, yearly/annually
//	Weekdays:       Mon, Monday, Mon,Wed,Fri, Mon..Fri
//	Date:           YYYY-MM-DD or MM-DD, each field taking *, a value, a
//	                list, a range (01..07), or a step (01/7, */2)
//	Day from end:   ~1 (last day of the month), ~3..~1 (last three days)
//	Time:           HH:MM[:SS] with the same *, list, range and step forms
//	Timezone:       a trailing UTC or IANA name (Europe/Bucharest)
//	Omitted time:   a weekday-only or date-only expression means midnight
//
// Out of scope: week-of-year, sub-second precision.
type CalendarSpec struct {
	weekdays []time.Weekday // nil = any
	years    []int          // nil = any
	months   []time.Month   // nil = any
	days     []int          // 1..31, nil = any
	// daysFromEnd holds `~N` entries: N counted back from the last day of
	// the month, so 1 is the last day. Resolved per month, since the
	// answer differs for February. nil = no such constraint; a spec may
	// carry both days and daysFromEnd, and a date matching either fires.
	daysFromEnd []int
	hours       []int // 0..23, nil = any
	minutes     []int // 0..59, nil = any
	seconds     []int // 0..59, nil = any
	// loc is the timezone the expression is written in. nil means "the
	// zone of the instant NextAfter is asked about", which for a running
	// daemon is local time.
	loc *time.Location
}

// ParseCalendar decodes a systemd-style OnCalendar expression.
func ParseCalendar(s string) (*CalendarSpec, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty calendar expression")
	}

	fields, loc, err := splitTimezone(strings.Fields(s))
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("calendar %q: timezone with no time", s)
	}

	if len(fields) == 1 {
		if spec, ok := calendarAlias(fields[0]); ok {
			spec.loc = loc
			return spec, nil
		}
	}

	spec := &CalendarSpec{loc: loc}

	// Optional weekday head: a bare word that looks like a day name.
	if isWeekdayPart(fields[0]) {
		wds, err := parseWeekdays(fields[0])
		if err != nil {
			return nil, err
		}
		spec.weekdays = wds
		fields = fields[1:]
	}

	// Optional date head. A time field never contains '-', so the marker
	// is unambiguous.
	if len(fields) > 0 && strings.Contains(fields[0], "-") {
		ys, ms, ds, dsEnd, err := parseDate(fields[0])
		if err != nil {
			return nil, err
		}
		spec.years, spec.months, spec.days, spec.daysFromEnd = ys, ms, ds, dsEnd
		fields = fields[1:]
	}

	switch len(fields) {
	case 0:
		// A weekday-only or date-only expression means midnight, matching
		// systemd: "Mon" is "Mon *-*-* 00:00:00". Reaching here with
		// neither a weekday nor a date is impossible — the input was
		// non-empty, so one of the two heads consumed a field.
		spec.hours, spec.minutes, spec.seconds = []int{0}, []int{0}, []int{0}
	case 1:
		hs, mn, sc, err := parseTime(fields[0])
		if err != nil {
			return nil, err
		}
		spec.hours, spec.minutes, spec.seconds = hs, mn, sc
	default:
		return nil, fmt.Errorf("calendar %q: expected one time field, got %d",
			s, len(fields))
	}
	return spec, nil
}

// calendarSearchDays bounds the forward search. Eight years covers the
// worst legitimate gap in an unconstrained spec — `*-02-29` waits up to
// four years — while still terminating on an impossible one like
// `*-02-30`. A spec that names distant years skips ahead by year instead
// of walking days, so the bound does not limit how far off those can be.
const calendarSearchDays = 366 * 8

// NextAfter returns the earliest instant strictly after `after` that
// matches the spec, or the zero time when there is none.
//
// The walk is over wall-clock date and time components, materialising a
// time.Time only for a candidate that matches. That ordering matters in a
// timezone with DST: constructing first and comparing afterwards would
// accept whatever the standard library normalised a nonexistent local
// time into.
func (c *CalendarSpec) NextAfter(after time.Time) time.Time {
	loc := c.loc
	if loc == nil {
		loc = after.Location()
	}
	// One second on, truncated, so a match exactly at `after` is not
	// returned again and successive calls make progress.
	start := after.In(loc).Add(time.Second).Truncate(time.Second)

	startY, startMo, startD := start.Date()
	startSOD := start.Hour()*3600 + start.Minute()*60 + start.Second()

	maxYear := 0
	if len(c.years) > 0 {
		maxYear = c.years[len(c.years)-1]
	}

	y, mo, d := startY, startMo, startD
	for i := 0; i < calendarSearchDays; i++ {
		if maxYear > 0 && y > maxYear {
			return time.Time{}
		}
		if c.years != nil && !containsExactly(c.years, y) {
			// Skip whole years rather than walking days through them, so
			// a spec naming 2040 does not exhaust the day budget.
			ny, ok := nextAllowed(c.years, y+1, maxYear)
			if !ok {
				return time.Time{}
			}
			y, mo, d = ny, time.January, 1
			continue
		}
		if c.dateAllowed(y, mo, d, loc) {
			minSOD := -1
			if y == startY && mo == startMo && d == startD {
				minSOD = startSOD
			}
			if t, ok := c.firstInstant(y, mo, d, minSOD, loc); ok {
				return t
			}
		}
		// Advance one day, letting the standard library roll the month
		// and year over. Noon keeps the arithmetic clear of DST edges.
		nd := time.Date(y, mo, d+1, 12, 0, 0, 0, loc)
		y, mo, d = nd.Date()
	}
	return time.Time{}
}

// dateAllowed reports whether a calendar date satisfies the month, day and
// weekday constraints. The year is checked by the caller, which can skip
// ahead on a mismatch.
func (c *CalendarSpec) dateAllowed(y int, mo time.Month, d int, loc *time.Location) bool {
	if !monthAllowed(c.months, mo) {
		return false
	}
	if !c.dayAllowed(y, mo, d) {
		return false
	}
	if len(c.weekdays) > 0 {
		// Noon: any instant inside the day gives the same weekday, and
		// midnight can be the moment a zone shifts.
		if !weekdayContains(c.weekdays, time.Date(y, mo, d, 12, 0, 0, 0, loc).Weekday()) {
			return false
		}
	}
	return true
}

// dayAllowed resolves both day forms. `~N` has to be resolved per month
// because the last day of February is not the last day of March.
func (c *CalendarSpec) dayAllowed(y int, mo time.Month, d int) bool {
	if c.days == nil && c.daysFromEnd == nil {
		return true
	}
	if containsExactly(c.days, d) {
		return true
	}
	if len(c.daysFromEnd) > 0 {
		last := daysInMonth(y, mo)
		for _, n := range c.daysFromEnd {
			if d == last-n+1 {
				return true
			}
		}
	}
	return false
}

// firstInstant returns the earliest matching instant on the given date
// whose second-of-day is at least minSOD (-1 for no floor).
//
// A candidate that the standard library normalises to different clock
// components did not exist in this zone — the hour the clocks skipped
// forward over. Such a candidate is passed over rather than fired at the
// substituted time, so `02:30 Europe/Bucharest` simply does not run on
// the spring-forward day instead of running at 03:30.
func (c *CalendarSpec) firstInstant(y int, mo time.Month, d, minSOD int, loc *time.Location) (time.Time, bool) {
	for _, h := range pickInts(c.hours, allHours) {
		if h*3600+3599 < minSOD {
			continue
		}
		for _, mi := range pickInts(c.minutes, all0to59) {
			if h*3600+mi*60+59 < minSOD {
				continue
			}
			for _, s := range pickInts(c.seconds, all0to59) {
				if h*3600+mi*60+s < minSOD {
					continue
				}
				t := time.Date(y, mo, d, h, mi, s, 0, loc)
				if t.Day() == d && t.Hour() == h && t.Minute() == mi && t.Second() == s {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// --- parsing helpers ---

// splitTimezone peels an optional trailing timezone field. Only a field
// that can be nothing else is treated as one: a '/' or one of the bare
// names the standard library knows. A stricter test than "the last field
// after the time" keeps `Mon 2027-01-01` reading as a date rather than as
// an unknown zone.
func splitTimezone(fields []string) ([]string, *time.Location, error) {
	if len(fields) < 2 {
		return fields, nil, nil
	}
	last := fields[len(fields)-1]
	if !looksLikeTimezone(last) {
		return fields, nil, nil
	}
	loc, err := time.LoadLocation(last)
	if err != nil {
		return nil, nil, fmt.Errorf("calendar: unknown timezone %q: %w", last, err)
	}
	return fields[:len(fields)-1], loc, nil
}

func looksLikeTimezone(s string) bool {
	if strings.Contains(s, "/") {
		return true
	}
	switch s {
	case "UTC", "GMT", "Local":
		return true
	}
	return false
}

func calendarAlias(s string) (*CalendarSpec, bool) {
	midnight := func(spec *CalendarSpec) *CalendarSpec {
		spec.hours, spec.minutes, spec.seconds = []int{0}, []int{0}, []int{0}
		return spec
	}
	switch strings.ToLower(s) {
	case "minutely":
		return &CalendarSpec{seconds: []int{0}}, true
	case "hourly":
		return &CalendarSpec{minutes: []int{0}, seconds: []int{0}}, true
	case "daily", "midnight":
		return midnight(&CalendarSpec{}), true
	case "weekly":
		return midnight(&CalendarSpec{weekdays: []time.Weekday{time.Monday}}), true
	case "monthly":
		return midnight(&CalendarSpec{days: []int{1}}), true
	case "quarterly":
		return midnight(&CalendarSpec{
			months: []time.Month{time.January, time.April, time.July, time.October},
			days:   []int{1},
		}), true
	case "semiannually", "semi-annually", "biannually":
		return midnight(&CalendarSpec{
			months: []time.Month{time.January, time.July},
			days:   []int{1},
		}), true
	case "yearly", "annually":
		return midnight(&CalendarSpec{
			months: []time.Month{time.January},
			days:   []int{1},
		}), true
	}
	return nil, false
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
	"sat": time.Saturday,
	// Full names, which systemd accepts interchangeably with the short
	// ones. "tues"/"thur" are not systemd spellings and stay rejected.
	"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
	"wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday,
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

// parseDate decodes `YYYY-MM-DD` or `MM-DD`, each field taking any form
// parseNumField accepts. An omitted year means every year, as in systemd.
func parseDate(s string) (years []int, months []time.Month, days []int, daysFromEnd []int, err error) {
	fields := strings.Split(s, "-")
	var yf, mf, df string
	switch len(fields) {
	case 3:
		yf, mf, df = fields[0], fields[1], fields[2]
	case 2:
		yf, mf, df = "*", fields[0], fields[1]
	default:
		return nil, nil, nil, nil,
			fmt.Errorf("calendar date %q: expected YYYY-MM-DD or MM-DD", s)
	}

	years, err = parseNumField(yf, 1970, 9999)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("calendar year %q: %w", yf, err)
	}
	ms, err := parseNumField(mf, 1, 12)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("calendar month %q: %w", mf, err)
	}
	for _, m := range ms {
		months = append(months, time.Month(m))
	}
	days, daysFromEnd, err = parseDayField(df)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("calendar day %q: %w", df, err)
	}
	return years, months, days, daysFromEnd, nil
}

// parseDayField splits the day-of-month field into forward days and
// `~N` days counted from the end of the month. The two can be mixed in
// one list, and a date matching either fires.
func parseDayField(s string) (days []int, fromEnd []int, err error) {
	if s == "*" {
		return nil, nil, nil
	}
	var forward, backward []string
	for _, item := range strings.Split(s, ",") {
		if !strings.HasPrefix(item, "~") {
			forward = append(forward, item)
			continue
		}
		// `~3..~1` is written low-to-high in calendar order — the 3rd-last
		// day up to the last — which is high-to-low counted from the end.
		// Put the endpoints back in ascending order before the shared
		// field parser sees them, or it reads a backwards range.
		backward = append(backward, reverseFromEndRange(strings.ReplaceAll(item, "~", "")))
	}
	if len(forward) > 0 {
		days, err = parseNumField(strings.Join(forward, ","), 1, 31)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(backward) > 0 {
		fromEnd, err = parseNumField(strings.Join(backward, ","), 1, 31)
		if err != nil {
			return nil, nil, err
		}
	}
	return days, fromEnd, nil
}

// reverseFromEndRange swaps a descending `a..b` into `b..a`, leaving a
// step suffix and anything that is not a range untouched.
func reverseFromEndRange(item string) string {
	body, suffix := item, ""
	if i := strings.Index(item, "/"); i >= 0 {
		body, suffix = item[:i], item[i:]
	}
	seg := strings.SplitN(body, "..", 2)
	if len(seg) != 2 {
		return item
	}
	a, err1 := strconv.Atoi(seg[0])
	b, err2 := strconv.Atoi(seg[1])
	if err1 != nil || err2 != nil || a <= b {
		return item
	}
	return strconv.Itoa(b) + ".." + strconv.Itoa(a) + suffix
}

// parseTime decodes `HH:MM[:SS]`. An omitted seconds field means :00, so
// `12:30` fires once a minute into the hour rather than sixty times.
func parseTime(s string) (hours, minutes, seconds []int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, nil, nil, fmt.Errorf("calendar time %q: expected HH:MM or HH:MM:SS", s)
	}
	hours, err = parseNumField(parts[0], 0, 23)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hours: %w", err)
	}
	minutes, err = parseNumField(parts[1], 0, 59)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("minutes: %w", err)
	}
	if len(parts) == 3 {
		seconds, err = parseNumField(parts[2], 0, 59)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("seconds: %w", err)
		}
	} else {
		seconds = []int{0}
	}
	return hours, minutes, seconds, nil
}

// parseNumField decodes one numeric component, shared by every date and
// time field so they all accept the same forms:
//
//	"*"          → nil (any)
//	"42"         → single value
//	"5,10,15"    → list
//	"8..18"      → inclusive range
//	"8..18/2"    → range with step
//	"*/15", "0/15" → step from the low bound
//	"5/10"       → step from 5 up to the high bound
//
// The result is sorted and deduplicated; out-of-range values are an
// error rather than being clamped, so a typo is reported at load time.
func parseNumField(s string, lo, hi int) ([]int, error) {
	if s == "*" {
		return nil, nil
	}
	seen := map[int]struct{}{}
	add := func(v int) error {
		if v < lo || v > hi {
			return fmt.Errorf("value %d out of range %d..%d", v, lo, hi)
		}
		seen[v] = struct{}{}
		return nil
	}

	for _, item := range strings.Split(s, ",") {
		if item == "" {
			return nil, fmt.Errorf("empty list item")
		}
		body, step := item, 0
		if i := strings.Index(item, "/"); i >= 0 {
			body = item[:i]
			v, err := strconv.Atoi(item[i+1:])
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("invalid step %q", item[i+1:])
			}
			step = v
		}

		from, to := 0, 0
		switch {
		case body == "*":
			from, to = lo, hi
		case strings.Contains(body, ".."):
			seg := strings.SplitN(body, "..", 2)
			a, err1 := strconv.Atoi(seg[0])
			b, err2 := strconv.Atoi(seg[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range %q", body)
			}
			if a > b {
				return nil, fmt.Errorf("range %q runs backwards", body)
			}
			from, to = a, b
		default:
			v, err := strconv.Atoi(body)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", body)
			}
			from = v
			// A bare value with a step counts up to the high bound;
			// without one it is just itself.
			if step > 0 {
				to = hi
			} else {
				to = v
			}
		}

		if step == 0 {
			step = 1
		}
		for v := from; v <= to; v += step {
			if err := add(v); err != nil {
				return nil, err
			}
		}
	}

	out := make([]int, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("matches nothing")
	}
	return out, nil
}

// --- small helpers ---

var allHours = intRange(0, 23)
var all0to59 = intRange(0, 59)

func intRange(lo, hi int) []int {
	out := make([]int, 0, hi-lo+1)
	for v := lo; v <= hi; v++ {
		out = append(out, v)
	}
	return out
}

// pickInts substitutes the full range for an unconstrained component, so
// the search walks one ordered slice either way.
func pickInts(sl, all []int) []int {
	if sl == nil {
		return all
	}
	return sl
}

func daysInMonth(y int, mo time.Month) int {
	// Day zero of the following month is the last day of this one.
	return time.Date(y, mo+1, 0, 12, 0, 0, 0, time.UTC).Day()
}

// containsExactly is the strict counterpart of containsInt: a nil slice
// matches nothing. Used where "unconstrained" is decided by the caller.
func containsExactly(slice []int, v int) bool {
	for _, x := range slice {
		if x == v {
			return true
		}
	}
	return false
}

func containsInt(slice []int, v int) bool {
	if slice == nil {
		return true
	}
	return containsExactly(slice, v)
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
