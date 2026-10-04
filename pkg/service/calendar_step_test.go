package service

import (
	"testing"
	"time"
)

// A stepped time field must parse, and fire, the same whether or not a
// date head or a weekday precedes it.
//
// It did not. splitTimezone identified a trailing timezone by "contains
// a slash", and a slash is also the step separator, so any expression
// whose last field was a stepped time was read as a zone name and
// rejected: `*-*-* *:*:*/5` failed with `unknown timezone "*:*:*/5"`.
// The bare `*:*:*/5` worked, because a single-field expression returns
// before splitTimezone is consulted — which is why this went unnoticed
// and why the equivalence, not a list of accepted strings, is the thing
// worth asserting.
//
// Found by acceptance case 42-cron-calendar on real hardware: the
// service never loaded, so `slinitctl status` reported an empty state
// and the sub-task fired zero times. Every slinit-check assertion in
// that same case passed, because none of them used a step.
func TestCalendarStepParsesWithAndWithoutADateHead(t *testing.T) {
	// sec=1 so the first hit of a 5-second step is unambiguous.
	base := time.Date(2026, 10, 5, 12, 0, 1, 0, time.UTC)

	for _, pair := range []struct{ bare, prefixed string }{
		{"*:*:*/5", "*-*-* *:*:*/5"},
		{"*:0/5", "*-*-* *:0/5"},
		{"*:0/10", "Mon..Fri *:0/10"},
	} {
		bareSpec, err := ParseCalendar(pair.bare)
		if err != nil {
			t.Fatalf("%q: %v", pair.bare, err)
		}
		pfxSpec, err := ParseCalendar(pair.prefixed)
		if err != nil {
			t.Errorf("%q was rejected while %q parses: %v — a date head or "+
				"weekday must not change whether a stepped time field is "+
				"understood", pair.prefixed, pair.bare, err)
			continue
		}
		// Parsing is not enough: the step has to survive into the
		// schedule. A spec that parses and then ignores its step fires
		// at the wrong times, which is the shape several timer bugs in
		// this package have had.
		at1, at2 := base, base
		for i := 0; i < 4; i++ {
			at1 = bareSpec.NextAfter(at1)
			at2 = pfxSpec.NextAfter(at2)
			if at1.IsZero() || at2.IsZero() {
				t.Fatalf("%q/%q: NextAfter returned zero at step %d",
					pair.bare, pair.prefixed, i)
			}
			if !at1.Equal(at2) {
				t.Errorf("%q fires at %s where %q fires at %s",
					pair.prefixed, at2.Format(time.RFC3339),
					pair.bare, at1.Format(time.RFC3339))
			}
		}
	}
}

// The step in seconds is every five seconds, not every minute or
// whatever a dropped step would collapse to. The acceptance case counts
// on at least two hits in twelve seconds.
func TestCalendarSecondsStepFiresEveryFiveSeconds(t *testing.T) {
	spec, err := ParseCalendar("*-*-* *:*:*/5")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 5, 12, 0, 1, 0, time.UTC)
	var prev time.Time
	for i := 0; i < 4; i++ {
		next := spec.NextAfter(at)
		if next.IsZero() {
			t.Fatalf("NextAfter went zero at step %d", i)
		}
		if next.Second()%5 != 0 {
			t.Errorf("fired at second %d, not a multiple of 5", next.Second())
		}
		if !prev.IsZero() && next.Sub(prev) != 5*time.Second {
			t.Errorf("gap between firings is %v, want 5s", next.Sub(prev))
		}
		prev, at = next, next
	}
}

// A real zone name must still be taken as one, or the fix would trade
// one bug for another.
func TestCalendarStillAcceptsATrailingTimezone(t *testing.T) {
	for _, e := range []string{
		"*-*-* 12:00 Europe/Bucharest",
		"*-*-* 12:00 UTC",
		"Mon..Fri 09:00 America/New_York",
	} {
		if _, err := ParseCalendar(e); err != nil {
			t.Errorf("%q: %v", e, err)
		}
	}
	if _, err := ParseCalendar("*-*-* 12:00 Not/AZone"); err == nil {
		t.Error("an unknown Area/City name should still be rejected")
	}
}
