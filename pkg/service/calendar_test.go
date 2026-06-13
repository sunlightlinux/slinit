package service

import (
	"testing"
	"time"
)

// Fixed reference time used by every test. UTC keeps tests reproducible.
// 2026-06-13 (Saturday) 12:34:56 UTC.
var refTime = time.Date(2026, time.June, 13, 12, 34, 56, 0, time.UTC)

func mustParse(t *testing.T, expr string) *CalendarSpec {
	t.Helper()
	c, err := ParseCalendar(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return c
}

func TestCalendarParseRejectsEmpty(t *testing.T) {
	if _, err := ParseCalendar(""); err == nil {
		t.Error("empty expression should error")
	}
}

func TestCalendarAliases(t *testing.T) {
	cases := []struct {
		expr string
		want time.Time
	}{
		// minutely → next 0-second boundary
		{"minutely", time.Date(2026, 6, 13, 12, 35, 0, 0, time.UTC)},
		// hourly → top of next hour
		{"hourly", time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)},
		// daily → midnight tomorrow
		{"daily", time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)},
		{"midnight", time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)},
		// weekly → Monday midnight; ref is Saturday → next Monday is 2026-06-15
		{"weekly", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)},
		// monthly → 1st of next month
		{"monthly", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		// yearly → Jan 1 next year
		{"yearly", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		spec := mustParse(t, c.expr)
		got := spec.NextAfter(refTime)
		if !got.Equal(c.want) {
			t.Errorf("%q: got %v want %v", c.expr, got, c.want)
		}
	}
}

func TestCalendarHHMM(t *testing.T) {
	// 02:00 — next day at 02:00 (ref is past 02:00 today).
	spec := mustParse(t, "02:00")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 14, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarHHMMTodayIfFuture(t *testing.T) {
	// 14:00 — same day, later today.
	spec := mustParse(t, "14:00")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 13, 14, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarMinutesStep(t *testing.T) {
	// *:0/15 = every 15 minutes (:00, :15, :30, :45) at second 0.
	// Ref is 12:34:56 → next match is 12:45:00.
	spec := mustParse(t, "*:0/15")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 13, 12, 45, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarWeekdayList(t *testing.T) {
	// Mon..Fri 09:00 — weekdays 9am. Ref is Saturday → next is Mon 2026-06-15 09:00.
	spec := mustParse(t, "Mon..Fri 09:00")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarSingleWeekday(t *testing.T) {
	// Sun 03:00 — Sunday at 3am. Ref is Sat → tomorrow (2026-06-14, Sun) 03:00.
	spec := mustParse(t, "Sun 03:00")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 14, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarCommaList(t *testing.T) {
	// Mon,Wed,Fri 12:00 — three specific weekdays.
	// Ref is Sat 12:34:56 → next is Mon 2026-06-15 12:00.
	spec := mustParse(t, "Mon,Wed,Fri 12:00")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarHHMMSS(t *testing.T) {
	// 03:30:45 — daily at exactly this time. Ref is past, next is tomorrow.
	spec := mustParse(t, "03:30:45")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 6, 14, 3, 30, 45, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarDateWildcards(t *testing.T) {
	// *-*-1 00:00 — first of every month. Ref is mid-June → next is July 1.
	spec := mustParse(t, "*-*-1 00:00")
	got := spec.NextAfter(refTime)
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestCalendarRejectsMalformed(t *testing.T) {
	bad := []string{
		"not-a-thing",
		"99:99",
		"Mon",                  // weekday without time
		"03:00:99",             // bad seconds
		"*-13-1 00:00",         // bad month
		"*-*-32 00:00",         // bad day
		"Foo 03:00",            // bad weekday
		"03:00 extra-trailing", // too many fields
	}
	for _, expr := range bad {
		if _, err := ParseCalendar(expr); err == nil {
			t.Errorf("%q: expected parse error", expr)
		}
	}
}

func TestCalendarNextAfterMonotonic(t *testing.T) {
	// Successive NextAfter calls must be strictly increasing.
	spec := mustParse(t, "*:0/15")
	t1 := spec.NextAfter(refTime)
	t2 := spec.NextAfter(t1)
	t3 := spec.NextAfter(t2)
	if !(t1.Before(t2) && t2.Before(t3)) {
		t.Errorf("not monotonic: %v < %v < %v?", t1, t2, t3)
	}
	// 15 min gaps between :15, :30, :45 etc.
	if d := t2.Sub(t1); d != 15*time.Minute {
		t.Errorf("gap1: got %v want 15m", d)
	}
	if d := t3.Sub(t2); d != 15*time.Minute {
		t.Errorf("gap2: got %v want 15m", d)
	}
}
