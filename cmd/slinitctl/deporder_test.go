package main

import "testing"

// Both argument orders for add-dep / rm-dep. slinitctl has always taken
// `<from> <dep-type> <to>`; dinit takes `<type> <from> <to>` and
// slinitctl(8) documented dinit's. Both work now, so neither set of
// scripts is the one that breaks.
func TestOrderDepArgsAcceptsBothOrders(t *testing.T) {
	cases := []struct {
		name         string
		a, b, c      string
		from, kd, to string
	}{
		{"slinitctl order", "web", "waits-for", "db", "web", "waits-for", "db"},
		{"dinit order", "waits-for", "web", "db", "web", "waits-for", "db"},
		{"slinitctl order, alias type", "web", "soft", "db", "web", "soft", "db"},
		{"dinit order, alias type", "regular", "web", "db", "web", "regular", "db"},
		{"ordering dep, dinit order", "before", "web", "db", "web", "before", "db"},
		{"ordering dep, slinitctl order", "web", "after", "db", "web", "after", "db"},
		{"prepared-by, dinit order", "prepared-by", "web", "db", "web", "prepared-by", "db"},

		// Neither position names a type: the middle one is passed through
		// so the caller reports the same "unknown dependency type" as before.
		{"no type anywhere", "web", "nonsense", "db", "web", "nonsense", "db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, kd, to := orderDepArgs(tc.a, tc.b, tc.c)
			if from != tc.from || kd != tc.kd || to != tc.to {
				t.Errorf("orderDepArgs(%q, %q, %q) = (%q, %q, %q), want (%q, %q, %q)",
					tc.a, tc.b, tc.c, from, kd, to, tc.from, tc.kd, tc.to)
			}
		})
	}
}

// A service named after a dependency type is the one ambiguous input.
// The middle position wins, because that is the order this CLI has always
// implemented and the one its usage string prints — so an operator who
// has a service called `waits-for` keeps the behaviour they had.
func TestOrderDepArgsPrefersTheMiddlePositionWhenBothCouldBeTypes(t *testing.T) {
	from, kd, to := orderDepArgs("waits-for", "before", "db")
	if from != "waits-for" || kd != "before" || to != "db" {
		t.Errorf("got (%q, %q, %q), want (waits-for, before, db) — the middle "+
			"position is the tie-break", from, kd, to)
	}
}

// Every name parseDepType accepts has to be recognised by the reorderer,
// or dinit-order invocations would silently be read as slinitctl-order
// with a service named after a type.
func TestOrderDepArgsKnowsEveryTypeName(t *testing.T) {
	for _, n := range []string{
		"depends-on", "regular", "waits-for", "soft",
		"depends-ms", "milestone", "prepared-by", "before", "after",
	} {
		from, kd, to := orderDepArgs(n, "web", "db")
		if from != "web" || kd != n || to != "db" {
			t.Errorf("type %q in first position was not recognised: got (%q, %q, %q)",
				n, from, kd, to)
		}
	}
}
