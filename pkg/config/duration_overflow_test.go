package config

import (
	"strings"
	"testing"
)

// Durations are seconds as a decimal number. ParseFloat also takes
// "inf"/"infinity" and "1e300", and anything past ~292 years converted
// to math.MinInt64 — a negative duration that slipped past the "< 0"
// check, which the service code then read as no timeout, or as every
// restart falling outside restart-limit-interval. They now saturate:
// "infinity" keeps meaning "as long as there is", and still loads, as
// it did. NaN is refused.
func TestDurationsSaturateInsteadOfWrapping(t *testing.T) {
	keys := []string{"stop-timeout", "start-timeout", "restart-limit-interval",
		"restart-delay", "restart-delay-step", "restart-delay-cap", "restart-randomized-delay"}
	for _, key := range keys {
		for _, v := range []string{"inf", "infinity", "+Inf", "1e300", "9300000000"} {
			src := "type = process\ncommand = /bin/true\n" + key + " = " + v + "\n"
			if _, err := Parse(strings.NewReader(src), "s", "s"); err != nil {
				t.Errorf("%s = %s refused: %v", key, v, err)
			}
		}
		src := "type = process\ncommand = /bin/true\n" + key + " = nan\n"
		if _, err := Parse(strings.NewReader(src), "s", "s"); err == nil {
			t.Errorf("%s = nan accepted", key)
		}
	}
	for _, v := range []string{"inf", "1e300", "9300000000"} {
		d, err := parseDuration(v)
		if err != nil || d != maxConfigDuration {
			t.Errorf("parseDuration(%q) = %v, %v; want the longest Duration", v, d, err)
		}
	}
	src := "type = process\ncommand = /bin/true\nrestart-limit-interval = 1e10\n"
	desc, err := Parse(strings.NewReader(src), "s", "s")
	if err != nil || desc.RestartInterval <= 0 {
		t.Errorf("restart-limit-interval = 1e10: %v, %v; want a positive interval", desc.RestartInterval, err)
	}
}

// The ceiling must leave room for the service code to add two saturated
// values — a restart delay and its step, or its jitter — without
// wrapping negative. 1<<62 itself does not: doubled it is 1<<63.
func TestSaturatedDurationsCanBeAddedTwice(t *testing.T) {
	d, err := parseDuration("infinity")
	if err != nil {
		t.Fatal(err)
	}
	if sum := d + d; sum < d {
		t.Errorf("two saturated durations add to %v: the ceiling has no headroom", sum)
	}
}
