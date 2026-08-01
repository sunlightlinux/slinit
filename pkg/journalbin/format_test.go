package journalbin

import (
	"bytes"
	"testing"
)

func TestObjectHeaderRoundtrip(t *testing.T) {
	orig := ObjectHeader{Type: ObjectData, Flags: 0, Size: 128}
	buf := make([]byte, ObjectHeaderSize)
	if err := orig.EncodeInto(buf); err != nil {
		t.Fatal(err)
	}
	// Reserved bytes at [2:8] must be zero-filled.
	for i := 2; i < 8; i++ {
		if buf[i] != 0 {
			t.Errorf("reserved byte at index %d not zero: %d", i, buf[i])
		}
	}
	got, err := DecodeObjectHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got != orig {
		t.Fatalf("roundtrip: got %+v want %+v", got, orig)
	}
}

func TestObjectHeaderShortBuffer(t *testing.T) {
	orig := ObjectHeader{Type: ObjectEntry, Size: 42}
	if err := orig.EncodeInto(make([]byte, 8)); err == nil {
		t.Fatal("expected error for short encode buffer")
	}
	if _, err := DecodeObjectHeader(make([]byte, 8)); err == nil {
		t.Fatal("expected error for short decode buffer")
	}
}

func TestObjectTypeString(t *testing.T) {
	cases := map[ObjectType]string{
		ObjectUnused:         "UNUSED",
		ObjectData:           "DATA",
		ObjectField:          "FIELD",
		ObjectEntry:          "ENTRY",
		ObjectDataHashTable:  "DATA_HASH_TABLE",
		ObjectFieldHashTable: "FIELD_HASH_TABLE",
		ObjectEntryArray:     "ENTRY_ARRAY",
		ObjectTag:            "TAG",
		42:                   "type(42)",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("ObjectType(%d).String() = %q, want %q", in, got, want)
		}
	}
}

func TestAlignUp(t *testing.T) {
	cases := []struct{ in, want uint64 }{
		{0, 0},
		{1, 8},
		{7, 8},
		{8, 8},
		{9, 16},
		{15, 16},
		{16, 16},
		{17, 24},
		{240, 240},
	}
	for _, c := range cases {
		if got := AlignUp(c.in); got != c.want {
			t.Errorf("AlignUp(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestHeaderRoundtrip(t *testing.T) {
	orig := NewHeader()
	orig.CompatFlags = CompatFlagSealed
	orig.State = StateOnline
	copy(orig.FileID[:], []byte("0123456789abcdef"))
	copy(orig.MachineID[:], []byte("fedcba9876543210"))
	copy(orig.BootID[:], []byte("11223344556677aa"))
	copy(orig.SeqnumID[:], []byte("aabbccddeeff0011"))
	orig.ArenaSize = 1_000_000
	orig.DataHashTableOffset = HeaderSize
	orig.DataHashTableSize = 233 * 16
	orig.FieldHashTableOffset = HeaderSize + 233*16
	orig.FieldHashTableSize = 233 * 16
	orig.TailObjectOffset = 999_000
	orig.NObjects = 500
	orig.NEntries = 100
	orig.TailEntrySeqnum = 200
	orig.HeadEntrySeqnum = 101
	orig.EntryArrayOffset = 4096
	orig.HeadEntryRealtime = 1_000_000_000
	orig.TailEntryRealtime = 2_000_000_000
	orig.TailEntryMonotonic = 500_000_000
	orig.NData = 300
	orig.NFields = 50
	orig.NTags = 10
	orig.NEntryArrays = 5

	buf := make([]byte, HeaderSize)
	if err := orig.EncodeInto(buf); err != nil {
		t.Fatal(err)
	}
	// Magic must be at offset 0.
	if !bytes.Equal(buf[0:8], Magic[:]) {
		t.Fatalf("magic at offset 0: got %q, want %q", buf[0:8], Magic[:])
	}
	// Reserved bytes at [17:24] must be zero.
	for i := 17; i < 24; i++ {
		if buf[i] != 0 {
			t.Errorf("reserved byte at %d not zero: %d", i, buf[i])
		}
	}

	got, err := DecodeHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *orig {
		t.Fatalf("roundtrip mismatch:\n got=%+v\nwant=%+v", got, orig)
	}
}

func TestDecodeHeaderRejectsBadMagic(t *testing.T) {
	buf := make([]byte, HeaderSize)
	copy(buf[0:8], []byte("NOTJRNL0"))
	if _, err := DecodeHeader(buf); err != ErrBadMagic {
		t.Fatalf("expected ErrBadMagic, got %v", err)
	}
}

func TestDecodeHeaderRejectsUnknownIncompat(t *testing.T) {
	h := NewHeader()
	// Bit outside the reserved 0x1|0x2|0x4 = 0x7 range.
	h.IncompatFlags = 0x100
	buf := make([]byte, HeaderSize)
	_ = h.EncodeInto(buf)
	if _, err := DecodeHeader(buf); err == nil {
		t.Fatal("expected error for unknown incompat flag")
	}
}

func TestDecodeHeaderRejectsClaimedIncompatFeatures(t *testing.T) {
	// Even a "known" incompat bit fails v1 since we don't implement any.
	h := NewHeader()
	h.IncompatFlags = IncompatFlagCompressedLZ4
	buf := make([]byte, HeaderSize)
	_ = h.EncodeInto(buf)
	if _, err := DecodeHeader(buf); err == nil {
		t.Fatal("expected error for claimed-but-unimplemented incompat")
	}
}

func TestDecodeHeaderShortBuffer(t *testing.T) {
	if _, err := DecodeHeader(make([]byte, 100)); err == nil {
		t.Fatal("expected error for short header buffer")
	}
}

func TestNewHeaderDefaults(t *testing.T) {
	h := NewHeader()
	if h.Magic != Magic {
		t.Fatal("Magic not set")
	}
	if h.HeaderSize != HeaderSize {
		t.Fatalf("HeaderSize = %d, want %d", h.HeaderSize, HeaderSize)
	}
	if h.State != StateOnline {
		t.Fatalf("State = %d, want %d", h.State, StateOnline)
	}
}
