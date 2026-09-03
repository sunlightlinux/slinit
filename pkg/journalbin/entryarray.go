package journalbin

import (
	"encoding/binary"
	"fmt"
	"os"
)

// ENTRY_ARRAY object layout on disk:
//   ObjectHeader(16) + next_entry_array_offset(8) + items[capacity](8 each)
//
// `next_entry_array_offset` chains arrays into a linked list, so a
// reader iterating in order calls `walk(arr) → arr.next` until reach
// zero. Each array's item count is derivable from its Size:
// (Size - ObjectHeaderSize - 8) / 8.

const (
	entryArrayFixedPart      = ObjectHeaderSize + 8 // header + next_offset
	entryArrayInitialCap     = 4
	entryArrayGrowthFactor   = 2
	entryArrayMaxCap         = 4096
	entryArrayItemStride     = 8
)

// maxEntryArrayObjectSize is the largest on-disk ENTRY_ARRAY object a
// legitimate writer will ever produce (header + next_off + max items).
// Used by readEntryArray as a hard cap against a hostile Size field.
// Without this bound, a Size of 2^61 in the header triggers a
// multi-EB alloc that OOMs the process. Caught by
// FuzzJournalBinaryOpenReader.
const maxEntryArrayObjectSize = uint64(entryArrayFixedPart + entryArrayMaxCap*entryArrayItemStride)

// entryArraySizeFor returns the total on-disk size (unpadded) of an
// ENTRY_ARRAY holding `cap` entries.
func entryArraySizeFor(cap int) uint64 {
	return uint64(entryArrayFixedPart + cap*entryArrayItemStride)
}

// entryArrayCapFrom recovers the capacity from the size field.
func entryArrayCapFrom(size uint64) int {
	if size < entryArrayFixedPart {
		return 0
	}
	return int(size-entryArrayFixedPart) / entryArrayItemStride
}

// allocateEntryArray writes an empty ENTRY_ARRAY of the given
// capacity at `at`. All entry slots and `next_entry_array_offset`
// are zero-initialised. Returns the padded byte size written.
func allocateEntryArray(f *os.File, at uint64, capacity int) (uint64, error) {
	sz := entryArraySizeFor(capacity)
	padded := AlignUp(sz)
	buf := make([]byte, padded)
	hdr := ObjectHeader{Type: ObjectEntryArray, Size: sz}
	if err := hdr.EncodeInto(buf); err != nil {
		return 0, err
	}
	if _, err := f.WriteAt(buf, int64(at)); err != nil {
		return 0, fmt.Errorf("journalbin: allocate entry array at %d: %w", at, err)
	}
	return padded, nil
}

// writeEntryArraySlot writes `entryOff` into slot `idx` of the array
// at `arrayOff`.
func writeEntryArraySlot(f *os.File, arrayOff uint64, idx int, entryOff uint64) error {
	off := int64(arrayOff) + int64(entryArrayFixedPart) + int64(idx*entryArrayItemStride)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], entryOff)
	if _, err := f.WriteAt(buf[:], off); err != nil {
		return fmt.Errorf("journalbin: write entry-array slot %d at %d: %w", idx, arrayOff, err)
	}
	return nil
}

// writeEntryArrayNext patches the next_entry_array_offset field of
// the array at `arrayOff` to point to `next`.
func writeEntryArrayNext(f *os.File, arrayOff, next uint64) error {
	off := int64(arrayOff) + int64(ObjectHeaderSize)
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], next)
	if _, err := f.WriteAt(buf[:], off); err != nil {
		return fmt.Errorf("journalbin: write entry-array next at %d: %w", arrayOff, err)
	}
	return nil
}

// readEntryArray fetches the header + next offset + entry offsets
// for the array at `arrayOff`. items returned may include trailing
// zeros for unfilled slots — the caller decides how far to consume
// based on the writer's per-array fill state (or, for a Reader
// walking the chain post-close, "up to the first zero" is a safe
// proxy since a partial fill can only occur at the tail).
func readEntryArray(f *os.File, arrayOff uint64) (next uint64, items []uint64, err error) {
	var hdrBuf [ObjectHeaderSize]byte
	if _, err := f.ReadAt(hdrBuf[:], int64(arrayOff)); err != nil {
		return 0, nil, fmt.Errorf("journalbin: read entry-array hdr at %d: %w", arrayOff, err)
	}
	oh, err := DecodeObjectHeader(hdrBuf[:])
	if err != nil {
		return 0, nil, err
	}
	if oh.Type != ObjectEntryArray {
		return 0, nil, fmt.Errorf("journalbin: expected ENTRY_ARRAY at %d, got %s", arrayOff, oh.Type)
	}
	if oh.Size < entryArrayFixedPart {
		return 0, nil, fmt.Errorf("journalbin: ENTRY_ARRAY at %d has size %d below minimum %d", arrayOff, oh.Size, entryArrayFixedPart)
	}
	if oh.Size > maxEntryArrayObjectSize {
		return 0, nil, fmt.Errorf("journalbin: ENTRY_ARRAY at %d has size %d exceeds max %d", arrayOff, oh.Size, maxEntryArrayObjectSize)
	}
	capacity := entryArrayCapFrom(oh.Size)
	body := make([]byte, oh.Size-ObjectHeaderSize)
	if _, err := f.ReadAt(body, int64(arrayOff)+int64(ObjectHeaderSize)); err != nil {
		return 0, nil, fmt.Errorf("journalbin: read entry-array body at %d: %w", arrayOff, err)
	}
	next = binary.LittleEndian.Uint64(body[0:8])
	items = make([]uint64, capacity)
	for i := 0; i < capacity; i++ {
		items[i] = binary.LittleEndian.Uint64(body[8+i*8 : 8+i*8+8])
	}
	return next, items, nil
}

// walkEntryOffsets calls fn once per non-zero entry offset in the
// chain starting at rootOff. Stops when fn returns false or the
// chain ends. rootOff == 0 walks nothing.
func walkEntryOffsets(f *os.File, rootOff uint64, fn func(uint64) bool) error {
	cur := rootOff
	for cur != 0 {
		next, items, err := readEntryArray(f, cur)
		if err != nil {
			return err
		}
		for _, e := range items {
			if e == 0 {
				// Trailing zero → we've reached the fill point of
				// this array; nothing beyond it in this array. If
				// there's a next array the loop below continues.
				break
			}
			if !fn(e) {
				return nil
			}
		}
		cur = next
	}
	return nil
}
