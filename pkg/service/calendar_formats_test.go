package service

import (
	"testing"
	"time"
)

// next is a table-driven shorthand: parse expr, ask for the first match
// after refTime, compare against a UTC wall clock.
func assertNext(t *testing.T, expr string, want time.Time) {
	t.Helper()
	got := mustParse(t, expr).NextAfter(refTime)
	if !got.Equal(want) {
		t.Errorf("%q: got %v want %v", expr, got, want)
	}
}

// The year used to be parsed and thrown away, so a one-shot date fired
// every year forever. These pin it down as a real constraint in both
// directions.
func TestCalendarYearConstraint(t *testing.T) {
	// refTime is 2026-06-13. A date later this year, and one in 2027.
	assertNext(t, "2026-12-25 06:00", time.Date(2026, 12, 25, 6, 0, 0, 0, time.UTC))
	assertNext(t, "2027-01-01 00:00", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))

	// A year already past has no next match at all — the whole point of
	// the constraint, and what the old code got wrong by firing annually.
	if got := mustParse(t, "2020-01-01 00:00").NextAfter(refTime); !got.IsZero() {
		t.Errorf("a date in the past must have no next match, got %v", got)
	}

	// A year far beyond the day-walk budget must still resolve, which it
	// only does because an unmatched year is skipped wholesale.
	assertNext(t, "2044-02-29 12:00", time.Date(2044, 2, 29, 12, 0, 0, 0, time.UTC))
}

// Feb 29 without a year: the next one is up to four years out, past what
// a two-year search window would have found.
func TestCalendarLeapDay(t *testing.T) {
	assertNext(t, "*-02-29 00:00", time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC))

	// An impossible date must terminate and report no match.
	if got := mustParse(t, "*-02-30 00:00").NextAfter(refTime); !got.IsZero() {
		t.Errorf("Feb 30 should never match, got %v", got)
	}
}

func TestCalendarDateListsRangesSteps(t *testing.T) {
	// List of months, first of each: next after mid-June is July 1.
	assertNext(t, "*-01,07-01 00:00", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	// Range of days: the first week of every month.
	assertNext(t, "*-*-01..07 09:00", time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC))
	// Step within the month: 1st, 8th, 15th, 22nd, 29th. Ref is the 13th,
	// so the next is the 15th.
	assertNext(t, "*-*-01/7 00:00", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	// Two-field date: the year is optional.
	assertNext(t, "07-04 12:00", time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC))
}

// `~N` counts back from the end of the month, which is why it cannot be
// rewritten as a fixed day: these three months have three different last
// days.
func TestCalendarDayFromEnd(t *testing.T) {
	assertNext(t, "*-*-~01 23:00", time.Date(2026, 6, 30, 23, 0, 0, 0, time.UTC))

	spec := mustParse(t, "*-*-~01 23:00")
	// June 30 → July 31 → August 31.
	t1 := spec.NextAfter(refTime)
	t2 := spec.NextAfter(t1)
	t3 := spec.NextAfter(t2)
	if t2.Day() != 31 || t2.Month() != time.July {
		t.Errorf("second fire: got %v want July 31", t2)
	}
	if t3.Day() != 31 || t3.Month() != time.August {
		t.Errorf("third fire: got %v want August 31", t3)
	}

	// February in a non-leap year: the 28th.
	if got := mustParse(t, "2027-02-~01 00:00").NextAfter(refTime); got.Day() != 28 {
		t.Errorf("Feb 2027 last day: got %v want the 28th", got)
	}
	// And in a leap year: the 29th.
	if got := mustParse(t, "2028-02-~01 00:00").NextAfter(refTime); got.Day() != 29 {
		t.Errorf("Feb 2028 last day: got %v want the 29th", got)
	}
	// A range counting back: the last three days of the month.
	assertNext(t, "*-*-~03..~01 00:00", time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC))
}

func TestCalendarTimeRanges(t *testing.T) {
	// Office hours, on the hour. Ref is 12:34:56 → 13:00.
	assertNext(t, "08..18:00", time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC))
	// Range with a step: every other hour from 08:00 → 14:00 next.
	assertNext(t, "08..18/2:00", time.Date(2026, 6, 13, 14, 0, 0, 0, time.UTC))
}

func TestCalendarFullWeekdayNames(t *testing.T) {
	// Ref is a Saturday; the short and long spellings must agree.
	short := mustParse(t, "Mon 09:00").NextAfter(refTime)
	long := mustParse(t, "Monday 09:00").NextAfter(refTime)
	if !short.Equal(long) {
		t.Errorf("Mon %v and Monday %v disagree", short, long)
	}
	assertNext(t, "Monday..Friday 09:00", time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC))
}

func TestCalendarNewAliases(t *testing.T) {
	// Ref is mid-June: next quarter starts July 1, next half-year too.
	assertNext(t, "quarterly", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	assertNext(t, "semiannually", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
}

// An expression with no time field means midnight, so these must not be
// parse errors — and must not accidentally mean "every second".
func TestCalendarOmittedTime(t *testing.T) {
	assertNext(t, "Mon", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC))
	assertNext(t, "2026-12-25", time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC))
	assertNext(t, "*-*-01", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
}

func TestCalendarTimezoneSuffix(t *testing.T) {
	// Bucharest is UTC+3 in June, so 12:00 local is 09:00 UTC. Asking
	// from a UTC reference proves the zone in the expression wins over
	// the zone of the instant being asked about.
	got := mustParse(t, "*-*-* 12:00 Europe/Bucharest").NextAfter(refTime)
	want := time.Date(2026, 6, 14, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v (%v UTC) want %v", got, got.UTC(), want)
	}

	// An explicit UTC suffix and no suffix agree when the reference is
	// already UTC.
	bare := mustParse(t, "03:00").NextAfter(refTime)
	utc := mustParse(t, "03:00 UTC").NextAfter(refTime)
	if !bare.Equal(utc) {
		t.Errorf("bare %v and UTC %v disagree", bare, utc)
	}

	if _, err := ParseCalendar("12:00 Mars/Olympus"); err == nil {
		t.Error("an unknown timezone should be a parse error")
	}
	// A date after a weekday must still read as a date, not be mistaken
	// for a zone name.
	if _, err := ParseCalendar("Fri 2026-06-19"); err != nil {
		t.Errorf("weekday + date should parse, got %v", err)
	}
}

// The spring-forward gap: on 2027-03-28 Bucharest jumps 03:00 → 04:00
// local, so 03:30 does not exist that day. The old construct-then-compare
// walk would have fired at whatever the standard library substituted.
func TestCalendarDSTSpringForwardSkipsMissingTime(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	// Confirm the premise rather than trusting it: 03:30 normalises away.
	if probe := time.Date(2027, 3, 28, 3, 30, 0, 0, loc); probe.Hour() == 3 {
		t.Skipf("2027-03-28 03:30 exists in this tzdata (%v); premise gone", probe)
	}

	spec := mustParse(t, "*-*-* 03:30 Europe/Bucharest")
	// Ask from the day before the transition.
	got := spec.NextAfter(time.Date(2027, 3, 27, 12, 0, 0, 0, loc))

	// The 27th still has an 03:30 in the future? No — 12:00 is past it,
	// so the next candidate is the 28th, which does not exist, so the
	// answer must be the 29th.
	want := time.Date(2027, 3, 29, 3, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %v, want the 29th (%v): the missing 03:30 must be skipped, not substituted",
			got, want)
	}
}

// The fall-back overlap: on 2027-10-31 Bucharest repeats 03:00 → 04:00
// local, so 03:30 happens twice. Firing twice would run a backup job
// twice; the rule is the first occurrence.
func TestCalendarDSTFallBackFiresOnce(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Bucharest")
	if err != nil {
		t.Fatalf("load zone: %v", err)
	}
	spec := mustParse(t, "*-*-* 03:30 Europe/Bucharest")

	first := spec.NextAfter(time.Date(2027, 10, 30, 12, 0, 0, 0, loc))
	if y, m, d := first.Date(); y != 2027 || m != time.October || d != 31 {
		t.Fatalf("expected the 31st, got %v", first)
	}
	// Whichever of the two 03:30s was chosen, the following match must be
	// the next day — not the same wall clock an hour later.
	second := spec.NextAfter(first)
	if second.Sub(first) < 23*time.Hour {
		t.Errorf("fired twice in the repeated hour: %v then %v (%v apart)",
			first, second, second.Sub(first))
	}
}

func TestCalendarRejectsNewMalformed(t *testing.T) {
	bad := []string{
		"*-*-01..07/0 00:00", // zero step
		"*-*-07..01 00:00",   // backwards range
		"*-*-1, 00:00",       // trailing empty list item
		"*-*-32..40 00:00",   // range entirely out of bounds
		"*-00-01 00:00",      // month zero
		"~1 00:00",           // day-from-end outside a date field
		"2026-13-01 00:00",   // month out of range
	}
	for _, expr := range bad {
		if _, err := ParseCalendar(expr); err == nil {
			t.Errorf("%q: expected parse error", expr)
		}
	}
}

// Every spec must make progress: an expression whose next match equalled
// its input would spin the cron loop.
func TestCalendarAlwaysAdvances(t *testing.T) {
	exprs := []string{
		"minutely", "hourly", "daily", "weekly", "monthly", "quarterly",
		"yearly", "*:0/15", "Mon..Fri 09:00", "*-*-~01 23:00",
		"*-*-01..07 09:00", "08..18/2:00", "*-*-* 12:00 Europe/Bucharest",
	}
	for _, expr := range exprs {
		spec := mustParse(t, expr)
		prev := refTime
		for i := 0; i < 4; i++ {
			got := spec.NextAfter(prev)
			if got.IsZero() {
				t.Errorf("%q: no match after %v", expr, prev)
				break
			}
			if !got.After(prev) {
				t.Errorf("%q: did not advance past %v (got %v)", expr, prev, got)
				break
			}
			prev = got
		}
	}
}
