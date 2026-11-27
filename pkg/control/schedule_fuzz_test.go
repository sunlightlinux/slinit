package control

import (
	"strings"
	"testing"
	"time"
)

// FuzzParseShutdownTime: a shutdown's time operand either is refused or
// gives a delay the wire can carry, and only a time that means "now"
// gives none. A word taken as zero is an immediate shutdown: slinitctl
// treated a mistyped time ("10:3O") as the message of an immediate
// reboot until 84b665f, and a delay that wrapped would read as "now" or
// as a few seconds (checkShutdownDelay's reason).
//
// "now" is fixed with non-zero seconds, so no HH:MM lands exactly on it
// and the only zero delays are spelled zeros.
func FuzzParseShutdownTime(f *testing.F) {
	for _, s := range []string{"", "now", "+0", "0", "+5", "10", "10:30", "10:3O",
		"23:59", "24:00", "+5min", "-5", "+-0", "99999999999", "+4294967296", "1:2", "now-ish"} {
		f.Add(s)
	}
	now := time.Date(2026, 11, 27, 10, 30, 17, 0, time.UTC)
	f.Fuzz(func(t *testing.T, s string) {
		d, err := ParseShutdownTime(s, now)
		if err != nil {
			return
		}
		if d < 0 || d > MaxShutdownDelay {
			t.Fatalf("ParseShutdownTime(%q) = %v, outside 0..%v", s, d, MaxShutdownDelay)
		}
		if d == 0 && !meansNow(s) {
			t.Fatalf("ParseShutdownTime(%q) = 0: an immediate shutdown from a word that does not mean now", s)
		}
		if d > 0 && d%time.Minute != 0 && !strings.Contains(s, ":") {
			t.Fatalf("ParseShutdownTime(%q) = %v: minutes are not whole", s, d)
		}
	})
}

// meansNow: "", "now", or a count of minutes that is zero, with at most
// the signs strconv.Atoi and the "+" prefix accept ("+0", "00", "-0").
func meansNow(s string) bool {
	if s == "" || s == "now" {
		return true
	}
	s = strings.TrimPrefix(s, "+")
	s = strings.TrimLeft(s, "+-")
	return s != "" && strings.Trim(s, "0") == ""
}
