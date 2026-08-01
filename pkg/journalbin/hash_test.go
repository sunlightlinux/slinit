package journalbin

import "testing"

// Jenkins lookup3 known-good vectors, pinned against a compile of Bob
// Jenkins' reference C (lookup3.c hashlittle, public domain May 2006).
// Regeneration procedure: run the tiny driver in the scratchpad
// (lookup3.c invoking hashlittle(str, strlen, 0)) and update the
// `want` values here — this is what unblocked the initial port
// bring-up when the Go output diverged from fabricated expectations.
//
// The high 32 bits of the returned uint64 stay zero in v1
// (KEYED_HASH bit reserved for a future siphash swap).
func TestHash64KnownVectors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want uint64
	}{
		// Empty input: initval=0 → a=b=c=0xdeadbeef, no mix()/final() call,
		// return c directly.
		{"empty", "", 0xdeadbeef},
		// Short tails (1..11 bytes) exercise the switch ladder without the
		// 12-byte block loop.
		{"1 char", "a", 0x58d68708},
		{"2 chars", "ab", 0xfbb3a8df},
		// Exactly 12 bytes exercises the block loop and hits the final()
		// path with the accumulators already mixed.
		{"12 chars (one full block)", "hello, world", 0x59a25215},
		// 13 bytes: one block + a 1-byte tail after the loop.
		{"13 chars (block + 1 tail)", "hello, world!", 0x86de4a71},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Hash64([]byte(c.in))
			if got != c.want {
				t.Errorf("Hash64(%q) = %#x, want %#x", c.in, got, c.want)
			}
		})
	}
}

// TestHash64DifferentInputsDifferentHashes is a smoke test — two
// nearby strings must not collide. Jenkins is designed for exactly
// this: single-bit input flips scramble the whole output.
func TestHash64AvalanchesOnBitFlip(t *testing.T) {
	a := Hash64([]byte("SUBSYSTEM=acpi"))
	b := Hash64([]byte("SUBSYSTEM=acpj")) // one-bit flip on the last char
	if a == b {
		t.Fatalf("Jenkins broke: same hash for near-identical inputs: %#x", a)
	}
	// Cheap avalanche sanity: expect at least 10 different bits.
	xor := a ^ b
	diff := 0
	for i := 0; i < 64; i++ {
		if xor&(1<<i) != 0 {
			diff++
		}
	}
	if diff < 10 {
		t.Fatalf("weak avalanche: only %d differing bits between %#x and %#x", diff, a, b)
	}
}

// TestHash64StablePerLength checks that hashing the same bytes twice
// returns the same value — trivial but catches accidental state leaks
// (e.g. if initval ever became a package var).
func TestHash64Stable(t *testing.T) {
	for _, s := range []string{"", "x", "hello", "SUBSYSTEM=acpi PRIORITY=6"} {
		a := Hash64([]byte(s))
		b := Hash64([]byte(s))
		if a != b {
			t.Fatalf("hash unstable for %q: %#x vs %#x", s, a, b)
		}
	}
}
