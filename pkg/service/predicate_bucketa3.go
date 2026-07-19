package service

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Bucket A3 — instantaneous PSI conditions. These are the read-once
// siblings of the v261 pressure watches (memory-pressure-watch etc.),
// which subscribe to threshold events at runtime. A predicate can be
// used to skip an expensive service when the system is already under
// load at start time; a watch cancels an already-running service when
// pressure crosses a threshold. Both live at /proc/pressure/*.
//
// The kernel exposes a two-line file per resource:
//
//	some avg10=<pct> avg60=<pct> avg300=<pct> total=<us>
//	full avg10=<pct> avg60=<pct> avg300=<pct> total=<us>
//
// Systemd defaults to the "some" line's avg10 field; we do the same so
// operators can port unit files unchanged. "some" means "at least one
// task stalled on this resource in the window", "full" means "every
// runnable task stalled". CPU only has "some".
//
// The parameter accepts an optional comparison operator (>=, <=, ==,
// >, <) followed by a floating-point percentage. Missing operator
// defaults to `>=` — the natural "gate when pressure exceeds N%"
// direction that dominates real usage. Reversing to `<` is fine for
// gating on quiet-system states.

func checkPSIPressure(path, param string) (bool, string) {
	op, rest := splitOp(strings.TrimSpace(param))
	if op == "==" && !strings.HasPrefix(strings.TrimSpace(param), "=") &&
		!strings.HasPrefix(strings.TrimSpace(param), "==") {
		// No explicit operator — flip the default to >= so
		// `memory-pressure = 50` reads as "pressure has been at
		// least 50% over the last 10s".
		op = ">="
	}
	want, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
	if err != nil {
		return false, fmt.Sprintf("psi: bad percent %q: %v", rest, err)
	}
	if want < 0 || want > 100 {
		return false, fmt.Sprintf("psi: percent %v out of [0,100]", want)
	}
	got, err := readPSISomeAvg10(path)
	if err != nil {
		return false, fmt.Sprintf("psi: %s: %v", path, err)
	}
	if evalFloatOp(got, op, want) {
		return true, ""
	}
	return false, fmt.Sprintf("psi: %s avg10 = %.2f%%, want %s %.2f%%", path, got, op, want)
}

// readPSISomeAvg10 returns the avg10 field of the "some" line as a
// percentage 0.0-100.0. Fails when the file is missing (kernel too old
// or PSI disabled) or the format is unexpected.
func readPSISomeAvg10(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, tok := range strings.Fields(line)[1:] {
			k, v, ok := strings.Cut(tok, "=")
			if !ok || k != "avg10" {
				continue
			}
			return strconv.ParseFloat(v, 64)
		}
	}
	return 0, fmt.Errorf("no `some avg10=` field")
}

func evalFloatOp(got float64, op string, want float64) bool {
	switch op {
	case ">":
		return got > want
	case ">=":
		return got >= want
	case "<":
		return got < want
	case "<=":
		return got <= want
	default:
		return got == want
	}
}
