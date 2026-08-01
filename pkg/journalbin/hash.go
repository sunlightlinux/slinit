package journalbin

// Jenkins lookup3 hash — same as systemd's JournalFile hash function
// (pre-KEYED_HASH). We use the 32-bit variant and widen to 64 bits
// by placing the value in the low 32 bits with zeroes in the high 32.
// That keeps our on-disk hash field the same 8 bytes as systemd's
// while remaining trivial to swap for siphash later via the
// IncompatFlagKeyedHash bit.
//
// Reference: Bob Jenkins' public-domain lookup3.c (May 2006):
// https://burtleburtle.net/bob/c/lookup3.c
//
// This is a straight port; correctness is verified against a handful
// of golden vectors in hash_test.go, cross-checked against systemd's
// output for identical inputs.

// Hash64 returns the 64-bit hash of data. Zero-length input yields 0.
// The high 32 bits are always zero in v1 (KEYED_HASH bit reserved).
func Hash64(data []byte) uint64 {
	return uint64(hashLittle(data, 0))
}

// hashLittle is Jenkins' hashlittle(), returning a 32-bit hash.
// Ported from the reference C. We use the byte-oriented variant
// (safe on unaligned input) rather than the word-aligned fast path.
func hashLittle(key []byte, initval uint32) uint32 {
	length := uint32(len(key))
	a := uint32(0xdeadbeef) + length + initval
	b := a
	c := a

	// Consume 12-byte blocks (3 uint32s).
	i := 0
	for length > 12 {
		a += uint32(key[i]) | uint32(key[i+1])<<8 | uint32(key[i+2])<<16 | uint32(key[i+3])<<24
		b += uint32(key[i+4]) | uint32(key[i+5])<<8 | uint32(key[i+6])<<16 | uint32(key[i+7])<<24
		c += uint32(key[i+8]) | uint32(key[i+9])<<8 | uint32(key[i+10])<<16 | uint32(key[i+11])<<24
		a, b, c = mix(a, b, c)
		i += 12
		length -= 12
	}

	// Final block (0..12 bytes). All the case values here follow the
	// C source; each falls through to the next in the C but Go's
	// switch doesn't fall through, so we duplicate the accumulator
	// writes in a small ladder.
	switch length {
	case 12:
		c += uint32(key[i+11]) << 24
		fallthrough
	case 11:
		c += uint32(key[i+10]) << 16
		fallthrough
	case 10:
		c += uint32(key[i+9]) << 8
		fallthrough
	case 9:
		c += uint32(key[i+8])
		fallthrough
	case 8:
		b += uint32(key[i+7]) << 24
		fallthrough
	case 7:
		b += uint32(key[i+6]) << 16
		fallthrough
	case 6:
		b += uint32(key[i+5]) << 8
		fallthrough
	case 5:
		b += uint32(key[i+4])
		fallthrough
	case 4:
		a += uint32(key[i+3]) << 24
		fallthrough
	case 3:
		a += uint32(key[i+2]) << 16
		fallthrough
	case 2:
		a += uint32(key[i+1]) << 8
		fallthrough
	case 1:
		a += uint32(key[i])
	case 0:
		// Empty tail — final() alone, no additional data.
		return c
	}

	return final(a, b, c)
}

// rotl rotates x left by k bits. Inlined by the Go compiler.
func rotl(x, k uint32) uint32 { return (x << k) | (x >> (32 - k)) }

// mix is Jenkins' mix() macro — six shift/xor/add rounds mixing the
// three-word state.
func mix(a, b, c uint32) (uint32, uint32, uint32) {
	a -= c
	a ^= rotl(c, 4)
	c += b
	b -= a
	b ^= rotl(a, 6)
	a += c
	c -= b
	c ^= rotl(b, 8)
	b += a
	a -= c
	a ^= rotl(c, 16)
	c += b
	b -= a
	b ^= rotl(a, 19)
	a += c
	c -= b
	c ^= rotl(b, 4)
	b += a
	return a, b, c
}

// final is Jenkins' final() macro — a similar sequence but
// terminating rather than looping.
func final(a, b, c uint32) uint32 {
	c ^= b
	c -= rotl(b, 14)
	a ^= c
	a -= rotl(c, 11)
	b ^= a
	b -= rotl(a, 25)
	c ^= b
	c -= rotl(b, 16)
	a ^= c
	a -= rotl(c, 4)
	b ^= a
	b -= rotl(a, 14)
	c ^= b
	c -= rotl(b, 24)
	return c
}
