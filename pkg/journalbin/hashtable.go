package journalbin

import (
	"encoding/binary"
	"fmt"
	"os"
)

// DefaultHashTableBuckets is the initial bucket count for both the
// DATA_HASH_TABLE and FIELD_HASH_TABLE. 233 matches systemd's
// default (prime, gives decent spread for the small-vocabulary
// field set journals typically hold). Rehash to grow is deferred to
// Phase B2b+ — a 233-bucket table with load factor of 4 still
// yields short chains at typical rates.
const DefaultHashTableBuckets = 233

// HashItemSize is the on-disk size of one bucket slot: head_offset(8)
// + tail_offset(8).
const HashItemSize = 16

// DATA object payload offsets (relative to the start of the DATA
// object, i.e. include the 16-byte ObjectHeader).
const (
	dataHashOffset            = ObjectHeaderSize       // 16
	dataNextHashOffset        = ObjectHeaderSize + 8   // 24
	dataNextFieldOffset       = ObjectHeaderSize + 16  // 32
	dataEntryOffsetOffset     = ObjectHeaderSize + 24  // 40
	dataEntryArrayOffsetOff   = ObjectHeaderSize + 32  // 48
	dataNEntriesOffset        = ObjectHeaderSize + 40  // 56
	dataPayloadStartOff       = ObjectHeaderSize + 48  // 64
)

// dataFixedPart is the number of bytes in a DATA object before the
// KEY=value payload. Used by size arithmetic on both write and read.
const dataFixedPart = dataPayloadStartOff

// allocateHashTable writes an empty hash table object at the given
// offset with `nBuckets` slots (all zero — every bucket empty). Used
// once at file creation for both DATA_HASH_TABLE and
// FIELD_HASH_TABLE. Returns the total bytes written (object header
// + slots + padding).
func allocateHashTable(f *os.File, at uint64, objType ObjectType, nBuckets int) (uint64, error) {
	tableSize := uint64(ObjectHeaderSize + nBuckets*HashItemSize)
	buf := make([]byte, AlignUp(tableSize))
	hdr := ObjectHeader{Type: objType, Size: tableSize}
	if err := hdr.EncodeInto(buf); err != nil {
		return 0, err
	}
	// Bucket slots are already zero in the freshly-allocated slice.
	if _, err := f.WriteAt(buf, int64(at)); err != nil {
		return 0, fmt.Errorf("journalbin: allocate hash table at %d: %w", at, err)
	}
	return AlignUp(tableSize), nil
}

// readHashItem returns (head, tail) for bucket `b` in the hash table
// whose object header starts at `tableStart`. Bucket index must be
// in [0, nBuckets).
func readHashItem(f *os.File, tableStart uint64, b int) (head, tail uint64, err error) {
	off := int64(tableStart) + int64(ObjectHeaderSize) + int64(b)*int64(HashItemSize)
	var buf [HashItemSize]byte
	if _, err := f.ReadAt(buf[:], off); err != nil {
		return 0, 0, fmt.Errorf("journalbin: read hash bucket %d: %w", b, err)
	}
	return binary.LittleEndian.Uint64(buf[0:8]),
		binary.LittleEndian.Uint64(buf[8:16]),
		nil
}

// writeHashItem persists (head, tail) for bucket `b` in the table at
// `tableStart`. Callers hold the writer's mutex.
func writeHashItem(f *os.File, tableStart uint64, b int, head, tail uint64) error {
	off := int64(tableStart) + int64(ObjectHeaderSize) + int64(b)*int64(HashItemSize)
	var buf [HashItemSize]byte
	binary.LittleEndian.PutUint64(buf[0:8], head)
	binary.LittleEndian.PutUint64(buf[8:16], tail)
	if _, err := f.WriteAt(buf[:], off); err != nil {
		return fmt.Errorf("journalbin: write hash bucket %d: %w", b, err)
	}
	return nil
}

// readDataHeader fetches the hash-chain metadata (hash + next_hash)
// and the KEY=value payload for the DATA object at `off`. Used by
// findOrInsertDataLocked when walking a bucket chain looking for a
// dedup hit. Returns hash, next, payload.
func readDataHeader(f *os.File, off uint64) (hash uint64, next uint64, payload []byte, err error) {
	var hdrBuf [dataPayloadStartOff]byte
	if _, err := f.ReadAt(hdrBuf[:], int64(off)); err != nil {
		return 0, 0, nil, fmt.Errorf("journalbin: read data hdr at %d: %w", off, err)
	}
	oh, err := DecodeObjectHeader(hdrBuf[:ObjectHeaderSize])
	if err != nil {
		return 0, 0, nil, err
	}
	if oh.Type != ObjectData {
		return 0, 0, nil, fmt.Errorf("journalbin: expected DATA at %d, got %s", off, oh.Type)
	}
	if oh.Size < dataFixedPart {
		return 0, 0, nil, fmt.Errorf("journalbin: DATA at %d size %d < fixed %d", off, oh.Size, dataFixedPart)
	}
	hash = binary.LittleEndian.Uint64(hdrBuf[dataHashOffset : dataHashOffset+8])
	next = binary.LittleEndian.Uint64(hdrBuf[dataNextHashOffset : dataNextHashOffset+8])
	payloadLen := int(oh.Size) - dataFixedPart
	if payloadLen == 0 {
		return hash, next, nil, nil
	}
	payload = make([]byte, payloadLen)
	if _, err := f.ReadAt(payload, int64(off)+int64(dataPayloadStartOff)); err != nil {
		return 0, 0, nil, fmt.Errorf("journalbin: read data payload at %d: %w", off, err)
	}
	return hash, next, payload, nil
}

// writeDataNextHash patches the next_hash field of the DATA object at
// `off`. Used to link a newly-inserted DATA onto the tail of a bucket
// chain.
func writeDataNextHash(f *os.File, off, next uint64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], next)
	if _, err := f.WriteAt(buf[:], int64(off)+int64(dataNextHashOffset)); err != nil {
		return fmt.Errorf("journalbin: patch next_hash at %d: %w", off, err)
	}
	return nil
}

// nBuckets returns the number of slots in the hash table at
// `tableStart`. Reads the object header's Size field.
func nBuckets(f *os.File, tableStart uint64) (int, error) {
	var hdrBuf [ObjectHeaderSize]byte
	if _, err := f.ReadAt(hdrBuf[:], int64(tableStart)); err != nil {
		return 0, fmt.Errorf("journalbin: read table hdr at %d: %w", tableStart, err)
	}
	oh, err := DecodeObjectHeader(hdrBuf[:])
	if err != nil {
		return 0, err
	}
	if oh.Type != ObjectDataHashTable && oh.Type != ObjectFieldHashTable {
		return 0, fmt.Errorf("journalbin: expected hash table at %d, got %s", tableStart, oh.Type)
	}
	if oh.Size < ObjectHeaderSize {
		return 0, fmt.Errorf("journalbin: hash table at %d size %d < header %d",
			tableStart, oh.Size, ObjectHeaderSize)
	}
	return int(oh.Size-ObjectHeaderSize) / HashItemSize, nil
}
