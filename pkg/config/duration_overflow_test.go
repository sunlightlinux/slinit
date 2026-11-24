package config

import (
	"strings"
	"testing"
)

// Durations are seconds as a decimal number. ParseFloat also takes
// "nan", "inf" and "1e300", and anything past ~292 years converted to
// math.MinInt64 — a negative duration that slipped past the "< 0"
// check, which the service code then read as no timeout, or as every
// restart falling outside restart-limit-interval.
func TestDurationsThatDoNotFitAreRefused(t *testing.T) {
	for _, key := range []string{"stop-timeout", "start-timeout", "restart-limit-interval",
		"restart-delay", "restart-delay-step", "restart-delay-cap", "restart-randomized-delay"} {
		for _, v := range []string{"nan", "inf", "+Inf", "1e300", "9300000000"} {
			src := "type = process\ncommand = /bin/true\n" + key + " = " + v + "\n"
			if _, err := Parse(strings.NewReader(src), "s", "s"); err == nil {
				t.Errorf("%s = %s accepted", key, v)
			}
		}
		src := "type = process\ncommand = /bin/true\n" + key + " = 1.5\n"
		if _, err := Parse(strings.NewReader(src), "s", "s"); err != nil {
			t.Errorf("%s = 1.5 refused: %v", key, err)
		}
	}
}
