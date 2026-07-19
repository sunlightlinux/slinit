package main

import "testing"

// TestParsePersonality covers the named domains and the numeric
// fallback. Unknown names surface as errors — a typo is worth catching
// visibly, since a silent fallback would leave the child in the wrong
// personality domain (a real correctness footgun for e.g. x86-on-x86-64
// binaries that need PER_LINUX32 alignment).
func TestParsePersonality(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"x86-64", 0},
		{"x86_64", 0},
		{"arm64", 0},
		{"aarch64", 0},
		{"x86", 0x0008},
		{"linux32", 0x0008},
		{"arm", 0x0008},
		{"0x800008", 0x800008},
		{"12", 12},
	}
	for _, tc := range cases {
		got, err := parsePersonality(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%q → 0x%x, want 0x%x", tc.in, got, tc.want)
		}
	}
	if _, err := parsePersonality("bogus"); err == nil {
		t.Errorf("expected error for bogus name")
	}
}

// TestWriteCoredumpFilterValidates: the value must be numeric (hex or
// dec) — anything else surfaces as an error rather than writing garbage
// to /proc/self/coredump_filter.
func TestWriteCoredumpFilterValidates(t *testing.T) {
	if err := writeCoredumpFilter("not-a-number"); err == nil {
		t.Errorf("expected error for non-numeric value")
	}
}
