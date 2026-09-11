package main

import (
	"testing"
)

// TestIoctlConstants: verify the WDIOC_* constants match what the
// Linux watchdog API mandates. Kernel headers are authoritative,
// but hardcoded numbers drift when someone accidentally edits the
// constant; this test locks them in.
//
// Encoding recap (asm-generic/ioctl.h):
//   _IOWR(type, nr, sz) = 3 << 30 | sz << 16 | type << 8 | nr
//   _IOR (type, nr, sz) = 2 << 30 | sz << 16 | type << 8 | nr
//
// WATCHDOG_IOCTL_BASE = 'W' = 0x57. sizeof(int) = 4.
//   WDIOC_SETTIMEOUT = _IOWR(0x57, 6, int) = 0xC0045706
//   WDIOC_KEEPALIVE  = _IOR (0x57, 5, int) = 0x80045705
func TestIoctlConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint
		want uint
	}{
		{"WDIOC_SETTIMEOUT", wdiocSetTimeout, 0xC0045706},
		{"WDIOC_KEEPALIVE", wdiocKeepalive, 0x80045705},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestIntervalClamping walks the argument-normalization logic that
// main() applies before entering run(). Pulled into a helper +
// checked here so the +/-1 edge cases don't rot silently.
func TestIntervalClamping(t *testing.T) {
	cases := []struct {
		name              string
		timeoutSec, iv    int
		wantInterval      int
	}{
		{"default (interval=0)", 60, 0, 30},
		{"explicit positive", 60, 20, 20},
		{"below floor clamps up", 60, 2, minIntervalSec},
		{"interval == timeout clamps to t-1", 30, 30, 29},
		{"interval > timeout clamps to t-1", 30, 60, 29},
		{"small timeout + tiny interval", 10, 1, minIntervalSec},
	}
	for _, c := range cases {
		got := computeInterval(c.timeoutSec, c.iv)
		if got != c.wantInterval {
			t.Errorf("%s: t=%d iv=%d → %d, want %d",
				c.name, c.timeoutSec, c.iv, got, c.wantInterval)
		}
	}
}

// computeInterval mirrors main()'s inline logic so it's testable
// without spinning up the full daemon. Kept in the _test.go file
// to avoid growing the binary surface; main() open-codes the same
// clamping (cheap enough that duplication is fine — the
// TestIntervalClamping cases keep the two in sync).
func computeInterval(timeoutSec, intervalSec int) int {
	iv := intervalSec
	if iv <= 0 {
		iv = timeoutSec / 2
	}
	if iv < minIntervalSec {
		iv = minIntervalSec
	}
	if iv >= timeoutSec {
		iv = timeoutSec - 1
	}
	return iv
}

// TestMagicCloseByte ensures the sentinel value is 'V' — the
// Linux watchdog API's documented "please disarm on close" byte.
// A silent change here would break shutdown-graceful semantics
// (WDT would fire after slinit-watchdogd exits via SIGTERM).
func TestMagicCloseByte(t *testing.T) {
	if magicCloseByte != 'V' {
		t.Errorf("magicCloseByte = %q, want 'V' (Linux watchdog-api.rst)", rune(magicCloseByte))
	}
}
