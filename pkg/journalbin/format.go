// Package journalbin implements the on-disk binary journal format
// used by slinit-journald when --format=binary. Structurally isomorphic
// to systemd-journald's format (same 7 object types, same 240-byte
// header layout, same hash+entry_array indexing scheme) but with a
// distinct magic (`SLJRNL01`) so `journalctl` from systemd cannot
// accidentally open slinit files and misparse them.
//
// The full spec lives in the memory file `project-journal-binary-format`.
// This package's public surface is intentionally small: Header
// encode/decode, Object type constants, Writer, Reader, and the
// sd_journal-semantic API under pkg/journalbin/sd.
//
// Coexistence with the JSONL sink from Phase C: both sinks satisfy
// pkg/journald.Sink so slinit-journald picks between them via
// --format=. Migration between formats is offered by cmd/slinit-
// journal-migrate.
package journalbin

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Magic is the 8-byte file signature at offset 0. Deliberately DIFFERENT
// from systemd-journald's LPKSHHRH so a stray `journalctl` invocation
// against a slinit file fails cleanly with "not a journal file" rather
// than misparsing.
var Magic = [8]byte{'S', 'L', 'J', 'R', 'N', 'L', '0', '1'}

// HeaderSize is the fixed size of the on-disk File Header. All objects
// start at offsets >= HeaderSize.
const HeaderSize = 240

// Alignment is the required alignment for every object start and size.
// Matches systemd's 8-byte convention so tag/hash arithmetic is
// straightforward.
const Alignment = 8

// State values for Header.State — track file lifecycle so a reader
// can tell whether a file is still being written to.
const (
	StateOffline  uint8 = 0 // clean, no writer
	StateOnline   uint8 = 1 // writer holds it
	StateArchived uint8 = 2 // rotated, immutable
)

// CompatFlag bits — reader may still open the file read-only if any
// unknown bit is set. Adding new bits stays backward-compat for
// readers that don't understand them.
const (
	CompatFlagSealed uint32 = 1 << 0 // FSS enabled (TAG objects present)
)

// IncompatFlag bits — reader MUST reject the file if any unknown bit
// is set. Only reserve bits for features slinit intends to grow into;
// premature reservation blocks future evolution.
const (
	IncompatFlagCompressedXZ  uint32 = 1 << 0 // reserved, unused in v1
	IncompatFlagCompressedLZ4 uint32 = 1 << 1 // reserved, unused in v1
	IncompatFlagKeyedHash     uint32 = 1 << 2 // reserved (siphash instead of jenkins)
)

// ObjectType identifies each object variant on disk. Numbers match
// systemd's convention 0..7 so external readers (once we ship an
// interop shim) can map identically.
type ObjectType uint8

const (
	ObjectUnused         ObjectType = 0
	ObjectData           ObjectType = 1
	ObjectField          ObjectType = 2
	ObjectEntry          ObjectType = 3
	ObjectDataHashTable  ObjectType = 4
	ObjectFieldHashTable ObjectType = 5
	ObjectEntryArray     ObjectType = 6
	ObjectTag            ObjectType = 7
)

// String returns a human name for the object type, used by --verify
// and diagnostic output. Unknown types render as "type(N)" so a
// forward-compat file with novel objects doesn't crash the printer.
func (t ObjectType) String() string {
	switch t {
	case ObjectUnused:
		return "UNUSED"
	case ObjectData:
		return "DATA"
	case ObjectField:
		return "FIELD"
	case ObjectEntry:
		return "ENTRY"
	case ObjectDataHashTable:
		return "DATA_HASH_TABLE"
	case ObjectFieldHashTable:
		return "FIELD_HASH_TABLE"
	case ObjectEntryArray:
		return "ENTRY_ARRAY"
	case ObjectTag:
		return "TAG"
	default:
		return fmt.Sprintf("type(%d)", uint8(t))
	}
}

// ObjectHeaderSize is the size of the common ObjectHeader that
// prefixes every object on disk (1 type + 1 flags + 6 reserved + 8 size).
const ObjectHeaderSize = 16

// ObjectHeader is the shared prefix on every object in a .journal file.
// Total on-disk size is ObjectHeaderSize (16) bytes; payload follows
// immediately after and its bytes-length is derivable as
// `Size - ObjectHeaderSize`.
type ObjectHeader struct {
	Type  ObjectType
	Flags uint8
	// 6 bytes reserved between Flags and Size — zero on write, ignored
	// on read. Not exposed as a field so callers can't accidentally
	// populate it.
	Size uint64 // total object size in bytes including this header, BEFORE alignment padding
}

// EncodeInto writes h at buf[0:ObjectHeaderSize]. buf must be at least
// ObjectHeaderSize bytes.
func (h ObjectHeader) EncodeInto(buf []byte) error {
	if len(buf) < ObjectHeaderSize {
		return fmt.Errorf("journalbin: object header buffer too small: %d < %d", len(buf), ObjectHeaderSize)
	}
	buf[0] = uint8(h.Type)
	buf[1] = h.Flags
	// Reserved 6 bytes at [2:8] zero-filled.
	for i := 2; i < 8; i++ {
		buf[i] = 0
	}
	binary.LittleEndian.PutUint64(buf[8:16], h.Size)
	return nil
}

// DecodeObjectHeader parses ObjectHeaderSize bytes at buf[0:] into an
// ObjectHeader. Rejects short buffers; does NOT validate Type against
// known values so a caller reading an unknown object still sees its
// declared Size and can skip it cleanly.
func DecodeObjectHeader(buf []byte) (ObjectHeader, error) {
	if len(buf) < ObjectHeaderSize {
		return ObjectHeader{}, fmt.Errorf("journalbin: object header buffer too small: %d < %d", len(buf), ObjectHeaderSize)
	}
	return ObjectHeader{
		Type:  ObjectType(buf[0]),
		Flags: buf[1],
		Size:  binary.LittleEndian.Uint64(buf[8:16]),
	}, nil
}

// AlignUp rounds n up to the next Alignment (8-byte) multiple. Every
// object on disk is padded to this so subsequent writes stay aligned.
func AlignUp(n uint64) uint64 {
	return (n + Alignment - 1) &^ (Alignment - 1)
}

// Header is the in-memory representation of the 240-byte File Header.
// Field order matches on-disk byte layout; use EncodeInto / DecodeHeader
// for wire I/O.
type Header struct {
	// Magic is copied from the constant on write and verified on read.
	Magic          [8]byte
	CompatFlags   uint32
	IncompatFlags uint32
	State         uint8
	// reserved [7]byte at offsets 17..23 (zero-fill)
	FileID     [16]byte // random UUID at file creation
	MachineID  [16]byte // from journal.MachineID() — hex string decoded
	BootID     [16]byte // from journal.BootID()
	SeqnumID   [16]byte // random per-file, forms the sd_journal cursor
	HeaderSize uint64   // fixed at HeaderSize
	ArenaSize  uint64   // bytes AFTER the header (== file_size - HeaderSize)

	DataHashTableOffset  uint64
	DataHashTableSize    uint64
	FieldHashTableOffset uint64
	FieldHashTableSize   uint64
	TailObjectOffset     uint64

	NObjects        uint64
	NEntries        uint64
	TailEntrySeqnum uint64
	HeadEntrySeqnum uint64
	EntryArrayOffset uint64

	HeadEntryRealtime   uint64
	TailEntryRealtime   uint64
	TailEntryMonotonic  uint64

	NData        uint64
	NFields      uint64
	NTags        uint64
	NEntryArrays uint64
}

// EncodeInto writes h at buf[0:HeaderSize]. buf must be at least
// HeaderSize bytes.
func (h *Header) EncodeInto(buf []byte) error {
	if len(buf) < HeaderSize {
		return fmt.Errorf("journalbin: header buffer too small: %d < %d", len(buf), HeaderSize)
	}
	// Zero the target region first so the reserved bytes at offset 17..23
	// end up zero without a per-byte loop.
	for i := 0; i < HeaderSize; i++ {
		buf[i] = 0
	}
	copy(buf[0:8], h.Magic[:])
	binary.LittleEndian.PutUint32(buf[8:12], h.CompatFlags)
	binary.LittleEndian.PutUint32(buf[12:16], h.IncompatFlags)
	buf[16] = h.State
	// buf[17:24] reserved — already zeroed above.
	copy(buf[24:40], h.FileID[:])
	copy(buf[40:56], h.MachineID[:])
	copy(buf[56:72], h.BootID[:])
	copy(buf[72:88], h.SeqnumID[:])
	binary.LittleEndian.PutUint64(buf[88:96], h.HeaderSize)
	binary.LittleEndian.PutUint64(buf[96:104], h.ArenaSize)
	binary.LittleEndian.PutUint64(buf[104:112], h.DataHashTableOffset)
	binary.LittleEndian.PutUint64(buf[112:120], h.DataHashTableSize)
	binary.LittleEndian.PutUint64(buf[120:128], h.FieldHashTableOffset)
	binary.LittleEndian.PutUint64(buf[128:136], h.FieldHashTableSize)
	binary.LittleEndian.PutUint64(buf[136:144], h.TailObjectOffset)
	binary.LittleEndian.PutUint64(buf[144:152], h.NObjects)
	binary.LittleEndian.PutUint64(buf[152:160], h.NEntries)
	binary.LittleEndian.PutUint64(buf[160:168], h.TailEntrySeqnum)
	binary.LittleEndian.PutUint64(buf[168:176], h.HeadEntrySeqnum)
	binary.LittleEndian.PutUint64(buf[176:184], h.EntryArrayOffset)
	binary.LittleEndian.PutUint64(buf[184:192], h.HeadEntryRealtime)
	binary.LittleEndian.PutUint64(buf[192:200], h.TailEntryRealtime)
	binary.LittleEndian.PutUint64(buf[200:208], h.TailEntryMonotonic)
	binary.LittleEndian.PutUint64(buf[208:216], h.NData)
	binary.LittleEndian.PutUint64(buf[216:224], h.NFields)
	binary.LittleEndian.PutUint64(buf[224:232], h.NTags)
	binary.LittleEndian.PutUint64(buf[232:240], h.NEntryArrays)
	return nil
}

// DecodeHeader parses HeaderSize bytes at buf[0:] into a Header.
// Verifies magic and rejects files with unknown IncompatFlags bits.
// Compat flags are surfaced without rejection so future readers can
// still open older-than-them files.
func DecodeHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderSize {
		return nil, fmt.Errorf("journalbin: header buffer too small: %d < %d", len(buf), HeaderSize)
	}
	h := &Header{}
	copy(h.Magic[:], buf[0:8])
	if h.Magic != Magic {
		return nil, ErrBadMagic
	}
	h.CompatFlags = binary.LittleEndian.Uint32(buf[8:12])
	h.IncompatFlags = binary.LittleEndian.Uint32(buf[12:16])
	// Reject any incompat bit we don't know. Reserved bits count as
	// unknown until we implement them.
	knownIncompat := IncompatFlagCompressedXZ | IncompatFlagCompressedLZ4 | IncompatFlagKeyedHash
	if h.IncompatFlags & ^knownIncompat != 0 {
		return nil, fmt.Errorf("journalbin: unknown incompat_flags 0x%x", h.IncompatFlags)
	}
	// v1 explicitly does NOT implement any incompat feature yet — if
	// the file claims one, we can't parse it.
	if h.IncompatFlags != 0 {
		return nil, fmt.Errorf("journalbin: file claims incompat features 0x%x that v1 cannot read", h.IncompatFlags)
	}
	h.State = buf[16]
	// buf[17:24] reserved — ignored.
	copy(h.FileID[:], buf[24:40])
	copy(h.MachineID[:], buf[40:56])
	copy(h.BootID[:], buf[56:72])
	copy(h.SeqnumID[:], buf[72:88])
	h.HeaderSize = binary.LittleEndian.Uint64(buf[88:96])
	h.ArenaSize = binary.LittleEndian.Uint64(buf[96:104])
	// Sanity: HeaderSize field must equal the constant. A future v2
	// might grow the header; if that happens the constant bumps and
	// this check adjusts.
	if h.HeaderSize != HeaderSize {
		return nil, fmt.Errorf("journalbin: header size on disk %d != expected %d", h.HeaderSize, HeaderSize)
	}
	h.DataHashTableOffset = binary.LittleEndian.Uint64(buf[104:112])
	h.DataHashTableSize = binary.LittleEndian.Uint64(buf[112:120])
	h.FieldHashTableOffset = binary.LittleEndian.Uint64(buf[120:128])
	h.FieldHashTableSize = binary.LittleEndian.Uint64(buf[128:136])
	h.TailObjectOffset = binary.LittleEndian.Uint64(buf[136:144])
	h.NObjects = binary.LittleEndian.Uint64(buf[144:152])
	h.NEntries = binary.LittleEndian.Uint64(buf[152:160])
	h.TailEntrySeqnum = binary.LittleEndian.Uint64(buf[160:168])
	h.HeadEntrySeqnum = binary.LittleEndian.Uint64(buf[168:176])
	h.EntryArrayOffset = binary.LittleEndian.Uint64(buf[176:184])
	h.HeadEntryRealtime = binary.LittleEndian.Uint64(buf[184:192])
	h.TailEntryRealtime = binary.LittleEndian.Uint64(buf[192:200])
	h.TailEntryMonotonic = binary.LittleEndian.Uint64(buf[200:208])
	h.NData = binary.LittleEndian.Uint64(buf[208:216])
	h.NFields = binary.LittleEndian.Uint64(buf[216:224])
	h.NTags = binary.LittleEndian.Uint64(buf[224:232])
	h.NEntryArrays = binary.LittleEndian.Uint64(buf[232:240])
	return h, nil
}

// NewHeader returns a fresh Header with Magic set + HeaderSize
// populated. Callers fill FileID/MachineID/BootID/SeqnumID from
// their own random / journal-pkg sources before persisting.
func NewHeader() *Header {
	return &Header{
		Magic:      Magic,
		HeaderSize: HeaderSize,
		State:      StateOnline,
	}
}

// Sentinel errors so callers can distinguish decode failures cleanly.
var (
	ErrBadMagic     = errors.New("journalbin: not a slinit journal file (bad magic)")
	ErrShortRead    = errors.New("journalbin: short read")
	ErrObjectBounds = errors.New("journalbin: object offset out of file bounds")
	ErrHashMismatch = errors.New("journalbin: hash chain corruption (hash mismatch)")
	ErrTagMismatch  = errors.New("journalbin: FSS tag mismatch (tamper or truncation)")
)
