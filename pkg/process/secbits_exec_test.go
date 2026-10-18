package process

import "testing"

// The exec-restriction securebits, added for systemd v262 parity.
//
// The bit numbers are the ones the kernel's securebits.h assigns
// (SECURE_EXEC_RESTRICT_FILE 8/9, SECURE_EXEC_DENY_INTERACTIVE 10/11)
// and this test states them as literals on purpose: a transcription
// slip would otherwise set a neighbouring securebit and quietly change
// what a service is allowed to do, which no amount of "it parses" would
// catch.
func TestExecRestrictionSecurebitsMapToTheKernelsBitNumbers(t *testing.T) {
	cases := map[string]uint32{
		"exec-restrict-file":           1 << 8,
		"exec-restrict-file-locked":    1 << 9,
		"exec-deny-interactive":        1 << 10,
		"exec-deny-interactive-locked": 1 << 11,
	}
	for name, want := range cases {
		got, err := ParseSecurebits(name)
		if err != nil {
			t.Errorf("ParseSecurebits(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSecurebits(%q) = %#x, want %#x", name, got, want)
		}
	}
}

// They must also combine with the pre-existing names rather than
// colliding with them: every securebit slinit knows has to occupy its
// own bit, or one name would silently imply another.
func TestEverySecurebitNameHasItsOwnBit(t *testing.T) {
	seen := map[uint32]string{}
	for name, bit := range secbitNames {
		if bit == 0 {
			t.Errorf("securebit %q maps to zero", name)
			continue
		}
		if other, dup := seen[bit]; dup {
			t.Errorf("securebits %q and %q share bit %#x", name, other, bit)
			continue
		}
		seen[bit] = name
	}
	if len(seen) != len(secbitNames) {
		t.Errorf("got %d distinct bits for %d names", len(seen), len(secbitNames))
	}
}
