package journalbin

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// Writer appends events to a .journal file in the on-disk binary
// format documented in project-journal-binary-format. It maintains
// an in-memory copy of the Header and rewrites it at offset 0 after
// each Append (or on Close), so a reader that opens the file mid-write
// sees a consistent view up to the last flushed entry.
//
// v1 (B1b) writes ENTRY + DATA objects only — no hash-table dedup,
// no entry-array index, no FSS. Those are wired in B2 and B3 without
// changing this file's public API; encoding stays additive.
//
// Safe for a single writer only. Concurrent Append calls are
// serialised by mu — journald has one goroutine per sink so that
// matches the deployed usage.
type Writer struct {
	mu     sync.Mutex
	f      *os.File
	header Header

	// entryItemsBuf reused across Appends to avoid allocations on
	// the hot path. Grown on demand.
	entryItemsBuf []byte

	// dataObjBuf reused for encoding a single DATA object header
	// + payload prefix.
	dataObjBuf []byte

	// seqnumID copied out of the header so we don't touch the struct
	// under mu during Cursor generation (B4 API).
	seqnumID [16]byte

	// nextSeqnum tracks the next ENTRY seqnum this file will assign.
	// Persisted in Header.TailEntrySeqnum after each flush.
	nextSeqnum uint64

	// Entry-array chain tail state — refreshed on reopen by walking
	// from Header.EntryArrayOffset. Kept in-mem to avoid re-reading
	// the tail array on every Append.
	entryArrayTail     uint64 // offset of last array (0 if none yet)
	entryArrayTailCap  int    // capacity of the tail array
	entryArrayTailFill int    // used slots in the tail array
	// forceNewArray flips to true after a TAG is written; the next
	// Append then allocates a fresh entry-array (linked to the
	// current tail) instead of patching a slot in the sealed array.
	// This preserves the FSS invariant: bytes covered by a written
	// TAG are never mutated afterwards.
	forceNewArray bool

	// FSS state. When fssKey is nil, sealing is disabled and no TAG
	// objects get written. When enabled, a TAG is written after
	// every fssTagEvery entries, at epoch boundaries, and on Close.
	fssKey             *FSSKey
	fssTagEvery        int
	fssLastTagEnd      uint64 // byte offset just past the last TAG (or HeaderSize if none)
	fssLastEpoch       int64  // -1 = no entries in this epoch yet
	fssEntriesSinceTag int
	fssNextTagSeqnum   uint64 // 1..
}

// WriterOptions collects everything a Writer needs beyond the file
// path. Kept as a struct (not positional args) so future additions
// (compression, per-file rotation, custom cursor namespace) don't
// churn every call site.
type WriterOptions struct {
	// BootID / MachineID: 32-lowercase-hex strings (systemd format),
	// same as journal.BootID() / journal.MachineID() output. Empty is
	// tolerated and stored as zero UUIDs (cursor loses its boot
	// component but the file stays valid).
	BootID    string
	MachineID string

	// FSSKey enables sealing. When nil, no TAG objects are written
	// and Header.CompatFlagSealed stays clear.
	FSSKey *FSSKey
	// TagEvery is the number of entries between TAG writes when FSS
	// is enabled. Zero picks DefaultFSSTagEvery. A TAG is also
	// written at every epoch boundary and on Close regardless of
	// this counter.
	TagEvery int
}

// DefaultFSSTagEvery is the number of entries between forced TAG
// writes when sealing is enabled. 32 balances two extremes: too
// few tags means a corrupt entry taints a long span of following
// entries (verifier reports the whole span bad); too many tags
// bloat the file. 32 matches systemd's --tag-every default.
const DefaultFSSTagEvery = 32

// NewWriter is the FSS-disabled convenience wrapper around
// NewWriterWithOptions. Kept for callers that don't need sealing —
// tests, the JSONL → binary migrator, and demo tooling.
func NewWriter(path, bootID, machineID string) (*Writer, error) {
	return NewWriterWithOptions(path, WriterOptions{
		BootID:    bootID,
		MachineID: machineID,
	})
}

// NewWriterWithOptions opens path for append, creating a new file
// with a fresh Header when the file is empty or missing, or reading
// the existing Header when the file already contains a journal.
// Returns an error if the existing file has a bad magic or an
// unknown incompat flag.
//
// When opts.FSSKey is non-nil, the writer seals appended entries by
// writing a TAG object every opts.TagEvery entries, at epoch
// boundaries, and on Close. The FSS state persists across
// reopen — recovery is based on Header.NTags + walking the last
// TAG to find its end offset, so an interrupted writer can resume
// sealing cleanly.
func NewWriterWithOptions(path string, opts WriterOptions) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0640)
	if err != nil {
		return nil, fmt.Errorf("journalbin: open %s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("journalbin: stat %s: %w", path, err)
	}

	w := &Writer{
		f:             f,
		entryItemsBuf: make([]byte, 0, 1024),
		dataObjBuf:    make([]byte, 0, 512),
	}

	if st.Size() == 0 {
		// Fresh file — mint header, allocate the two hash tables
		// right after it, then persist.
		hdr := NewHeader()
		hdr.HeaderSize = HeaderSize
		hdr.HeadEntrySeqnum = 0
		hdr.TailEntrySeqnum = 0
		if err := fillUUID(hdr.FileID[:]); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := fillUUID(hdr.SeqnumID[:]); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := decodeUUIDHex(opts.BootID, hdr.BootID[:]); err != nil {
			// Empty bootID / bad hex → zero UUID. Journal is still
			// valid but cursors lose the boot component.
		}
		if err := decodeUUIDHex(opts.MachineID, hdr.MachineID[:]); err != nil {
			// Same as above.
		}
		if opts.FSSKey != nil {
			hdr.CompatFlags |= CompatFlagSealed
		}

		// Layout right after the 240-byte header:
		//   [HeaderSize..dataTableEnd) DATA_HASH_TABLE
		//   [dataTableEnd..fieldTableEnd) FIELD_HASH_TABLE
		// After that, objects (DATA/ENTRY/...) grow from tail.
		dataTableStart := uint64(HeaderSize)
		dataTableBytes, err := allocateHashTable(f, dataTableStart, ObjectDataHashTable, DefaultHashTableBuckets)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		fieldTableStart := dataTableStart + dataTableBytes
		fieldTableBytes, err := allocateHashTable(f, fieldTableStart, ObjectFieldHashTable, DefaultHashTableBuckets)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		hdr.DataHashTableOffset = dataTableStart
		hdr.DataHashTableSize = dataTableBytes
		hdr.FieldHashTableOffset = fieldTableStart
		hdr.FieldHashTableSize = fieldTableBytes
		hdr.NObjects = 2 // DATA_HASH_TABLE + FIELD_HASH_TABLE
		hdr.TailObjectOffset = fieldTableStart + fieldTableBytes
		hdr.ArenaSize = hdr.TailObjectOffset - HeaderSize

		w.header = *hdr
		if err := w.writeHeaderLocked(); err != nil {
			_ = f.Close()
			return nil, err
		}
		w.nextSeqnum = 1
	} else {
		// Existing file — read header, verify.
		hdrBuf := make([]byte, HeaderSize)
		if _, err := f.ReadAt(hdrBuf, 0); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("journalbin: read header %s: %w", path, err)
		}
		existing, err := DecodeHeader(hdrBuf)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("journalbin: header %s: %w", path, err)
		}
		w.header = *existing
		// TailObjectOffset points at the next append slot; if a prior
		// writer crashed without updating it, we fall back to file size
		// (best-effort recovery — B5 would add a proper journalctl
		// --verify path).
		if w.header.TailObjectOffset < HeaderSize {
			w.header.TailObjectOffset = uint64(st.Size())
		}
		w.header.State = StateOnline
		w.nextSeqnum = w.header.TailEntrySeqnum + 1

		// Recover entry-array tail by walking the chain from the root.
		// A prior clean writer left the last array partially full; we
		// scan forward until the last non-zero slot to know where to
		// resume appending. Empty chain (no entries yet) is the
		// common fresh-daemon case.
		if err := w.recoverEntryArrayTailLocked(); err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	copy(w.seqnumID[:], w.header.SeqnumID[:])

	// FSS state init. TagEvery=0 picks the default; a non-nil key
	// with TagEvery<0 means "epoch-only" tags (never triggered by
	// counter). fssLastTagEnd starts at HeaderSize for a fresh file
	// (nothing has been sealed yet); for a reopen we recover by
	// scanning for the last TAG object.
	w.fssKey = opts.FSSKey
	w.fssTagEvery = opts.TagEvery
	if w.fssTagEvery == 0 {
		w.fssTagEvery = DefaultFSSTagEvery
	}
	w.fssLastEpoch = -1
	if w.fssKey != nil {
		if err := w.recoverFSSStateLocked(); err != nil {
			_ = f.Close()
			return nil, err
		}
	} else {
		w.fssLastTagEnd = HeaderSize
	}
	return w, nil
}

// recoverFSSStateLocked walks the file to find the last TAG object,
// setting fssLastTagEnd to point just past it (or HeaderSize when
// no TAG has been written yet). Also seeds fssNextTagSeqnum from
// the last TAG's seqnum + 1.
//
// Called on reopen from NewWriterWithOptions when FSS is enabled.
// Linear scan — acceptable because reopen is a rare (per-file-open)
// event; steady-state Append has no FSS-recovery cost.
func (w *Writer) recoverFSSStateLocked() error {
	w.fssLastTagEnd = HeaderSize
	w.fssNextTagSeqnum = 1
	// If the file's header claims no TAGs yet, nothing to recover.
	if w.header.NTags == 0 {
		return nil
	}
	// Scan for the last TAG. On a linear-only-appends file this ends
	// at the last-written TAG's aligned end offset.
	//
	// Truncation tolerance (same shape as recoverEntryArrayTailLocked):
	// header.TailObjectOffset may point past the actual file end after
	// an unclean shutdown, so the ReadAt calls below can return EOF
	// mid-scan. When they do, stop the scan and commit whatever
	// last-TAG we saw so far — losing FSS seal continuity for the
	// truncated tail is preferable to crash-looping the daemon.
	// Similar EOF handling on the tag-seqnum secondary read: skip
	// that specific tag's seqnum recovery, keep whatever we already
	// have.
	off := uint64(HeaderSize)
	var lastTagEnd uint64
	var lastSeqnum uint64
	fileSize := w.header.TailObjectOffset
	if fi, err := w.f.Stat(); err == nil && uint64(fi.Size()) < fileSize {
		// Physical file shorter than header claims — cap the scan
		// at the actual size so we never try to ReadAt past EOF
		// in the first place.
		fileSize = uint64(fi.Size())
	}
	for off < fileSize {
		var hdrBuf [ObjectHeaderSize]byte
		if _, err := w.f.ReadAt(hdrBuf[:], int64(off)); err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), ": EOF") {
				break
			}
			return fmt.Errorf("journalbin: FSS recovery read at %d: %w", off, err)
		}
		oh, err := DecodeObjectHeader(hdrBuf[:])
		if err != nil {
			return err
		}
		if oh.Size < ObjectHeaderSize {
			return fmt.Errorf("journalbin: FSS recovery bad object size %d at %d", oh.Size, off)
		}
		next := off + AlignUp(oh.Size)
		if next <= off || next > fileSize {
			// Wrap-safe forward-progress + no-past-EOF guard —
			// consistent with reader.Iter's defence. Stop the scan
			// here; last good tag (if any) becomes our tail.
			break
		}
		if oh.Type == ObjectTag {
			lastTagEnd = next
			var seqBuf [8]byte
			if _, err := w.f.ReadAt(seqBuf[:], int64(off)+int64(tagSeqnumOffset)); err != nil {
				if errors.Is(err, io.EOF) || strings.Contains(err.Error(), ": EOF") {
					break
				}
				return fmt.Errorf("journalbin: FSS recovery tag seqnum at %d: %w", off, err)
			}
			lastSeqnum = binary.LittleEndian.Uint64(seqBuf[:])
		}
		off = next
	}
	if lastTagEnd > 0 {
		w.fssLastTagEnd = lastTagEnd
	}
	if lastSeqnum > 0 {
		w.fssNextTagSeqnum = lastSeqnum + 1
	}
	return nil
}

// recoverEntryArrayTailLocked walks the ENTRY_ARRAY chain from the
// root, following next_entry_array_offset until it reaches the last
// array. Records the tail offset, its capacity, and how many slots
// are filled (first zero == fill boundary). No-op when the chain is
// empty (no entries yet).
//
// Truncation tolerance: when readEntryArray fails with io.EOF (the
// chain points into a region that got cut off — typical after an
// unclean shutdown), we treat the chain as terminated at the previous
// good offset instead of aborting startup. If the corruption is at
// the very first array (EntryArrayOffset itself past-EOF), the header
// gets reset to 0 so the next Append allocates a fresh chain. Cost:
// entries in unreadable segments become orphaned (already unreadable
// via forward walking anyway), but the daemon starts and keeps
// writing. Without this, slinit-journald crash-loops on any journal
// file that survived a hard reboot mid-write.
func (w *Writer) recoverEntryArrayTailLocked() error {
	if w.header.EntryArrayOffset == 0 {
		return nil
	}
	cur := w.header.EntryArrayOffset
	var lastGood uint64
	for {
		next, items, err := readEntryArray(w.f, cur)
		if err != nil {
			if errors.Is(err, io.EOF) || strings.Contains(err.Error(), ": EOF") {
				// Truncated chain. If we made no forward progress
				// (first array is past-EOF), forget the chain
				// entirely — Append will allocate a fresh one.
				if lastGood == 0 {
					w.header.EntryArrayOffset = 0
					w.entryArrayTail = 0
					w.entryArrayTailCap = 0
					w.entryArrayTailFill = 0
					return nil
				}
				// Otherwise the last good array is our new tail —
				// re-read it to populate tail state.
				_, items, err = readEntryArray(w.f, lastGood)
				if err != nil {
					return err
				}
				fill := 0
				for i, e := range items {
					if e == 0 {
						fill = i
						break
					}
					fill = i + 1
				}
				w.entryArrayTail = lastGood
				w.entryArrayTailCap = len(items)
				w.entryArrayTailFill = fill
				return nil
			}
			return err
		}
		if next == 0 {
			// Tail array. Fill count = index of first zero slot.
			fill := 0
			for i, e := range items {
				if e == 0 {
					fill = i
					break
				}
				fill = i + 1
			}
			w.entryArrayTail = cur
			w.entryArrayTailCap = len(items)
			w.entryArrayTailFill = fill
			return nil
		}
		lastGood = cur
		cur = next
	}
}

// Append writes one journal.Event as a series of DATA objects plus
// one ENTRY object referencing them. Trusted metadata already stamped
// by the caller (journal.Emit → daemon's stampFromSCM path) — this
// function does NOT re-derive PID/UID/etc. Returns the entry's offset
// so cursor computation in the sd_journal API can pin it.
func (w *Writer) Append(evt *journal.Event) (uint64, error) {
	if evt == nil {
		return 0, fmt.Errorf("journalbin: append nil event")
	}
	fields := eventFields(evt)
	if len(fields) == 0 {
		// Skip empty events — an ENTRY with zero items is legal on
		// disk but pointless.
		return 0, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// For each field: dedup against the DATA hash table. If a matching
	// DATA already lives on disk (same hash + same payload bytes),
	// reuse its offset; else append a new DATA object and link it
	// onto the bucket chain.
	items := w.entryItemsBuf[:0]
	dataOffsets := make([]uint64, 0, len(fields))
	for _, kv := range fields {
		off, hash, err := w.findOrInsertDataLocked(kv)
		if err != nil {
			return 0, err
		}
		items = binary.LittleEndian.AppendUint64(items, off)
		items = binary.LittleEndian.AppendUint64(items, hash)
		dataOffsets = append(dataOffsets, off)
	}
	w.entryItemsBuf = items

	// Compose ENTRY object: header(16) + seqnum(8) + realtime(8) +
	// monotonic(8) + boot_id(16) + xor_hash(8) + items(N*16).
	// XOR-hash: XOR of every DATA hash — a cheap integrity marker
	// like systemd uses; if an item offset gets scrambled the xor
	// mismatches and reader flags the entry.
	var xorHash uint64
	for i := 0; i < len(items); i += 16 {
		xorHash ^= binary.LittleEndian.Uint64(items[i+8 : i+16])
	}

	const entryFixedSize = ObjectHeaderSize + 8 + 8 + 8 + 16 + 8
	entrySize := uint64(entryFixedSize + len(items))
	buf := make([]byte, entrySize)
	entryHdr := ObjectHeader{Type: ObjectEntry, Size: entrySize}
	if err := entryHdr.EncodeInto(buf); err != nil {
		return 0, err
	}
	off := ObjectHeaderSize
	binary.LittleEndian.PutUint64(buf[off:off+8], w.nextSeqnum)
	off += 8
	binary.LittleEndian.PutUint64(buf[off:off+8], uint64(evt.Ts/1000)) // microseconds
	off += 8
	binary.LittleEndian.PutUint64(buf[off:off+8], uint64(evt.Mts/1000))
	off += 8
	// boot_id: decode from evt.BootID or use header's copy as fallback.
	var bootID [16]byte
	if err := decodeUUIDHex(evt.BootID, bootID[:]); err != nil {
		bootID = w.header.BootID
	}
	copy(buf[off:off+16], bootID[:])
	off += 16
	binary.LittleEndian.PutUint64(buf[off:off+8], xorHash)
	off += 8
	copy(buf[off:], items)

	entryOffset := w.header.TailObjectOffset
	if _, err := w.f.WriteAt(buf, int64(entryOffset)); err != nil {
		return 0, fmt.Errorf("journalbin: write entry at %d: %w", entryOffset, err)
	}

	// Pad to alignment.
	padded := AlignUp(entrySize)
	if padded > entrySize {
		pad := make([]byte, padded-entrySize)
		if _, err := w.f.WriteAt(pad, int64(entryOffset)+int64(entrySize)); err != nil {
			return 0, fmt.Errorf("journalbin: pad entry: %w", err)
		}
	}

	// Header updates for the ENTRY itself.
	w.header.TailObjectOffset = entryOffset + padded
	w.header.NObjects++
	w.header.NEntries++

	// Index the entry in the ENTRY_ARRAY chain so bisect on realtime
	// works without a linear scan.
	if err := w.appendEntryOffsetLocked(entryOffset); err != nil {
		return 0, err
	}

	// FSS: seal on epoch boundary or after fssTagEvery entries. The
	// TAG lands AFTER this entry, so the HMAC covers the entry.
	if w.fssKey != nil {
		curEpoch := w.fssKey.EpochFor(int64(evt.Ts / 1000))
		// First entry ever: seed epoch tracking without a boundary tag.
		if w.fssLastEpoch < 0 {
			w.fssLastEpoch = curEpoch
			w.fssEntriesSinceTag = 1
		} else if curEpoch != w.fssLastEpoch {
			// Epoch changed: seal everything up to (and including) the
			// just-written entry with the OLD epoch's key. Then advance.
			if err := w.writeSealTagLocked(w.fssLastEpoch); err != nil {
				return 0, err
			}
			w.fssLastEpoch = curEpoch
			w.fssEntriesSinceTag = 1
		} else {
			w.fssEntriesSinceTag++
			if w.fssEntriesSinceTag >= w.fssTagEvery {
				if err := w.writeSealTagLocked(curEpoch); err != nil {
					return 0, err
				}
				w.fssEntriesSinceTag = 0
			}
		}
	}
	w.header.TailEntrySeqnum = w.nextSeqnum
	if w.header.HeadEntrySeqnum == 0 {
		w.header.HeadEntrySeqnum = w.nextSeqnum
		w.header.HeadEntryRealtime = uint64(evt.Ts / 1000)
	}
	w.header.TailEntryRealtime = uint64(evt.Ts / 1000)
	w.header.TailEntryMonotonic = uint64(evt.Mts / 1000)
	w.header.ArenaSize = w.header.TailObjectOffset - HeaderSize
	if err := w.writeHeaderLocked(); err != nil {
		return 0, err
	}

	w.nextSeqnum++
	_ = dataOffsets // reserved for entry-array wiring in B2b
	return entryOffset, nil
}

// findOrInsertDataLocked returns the on-disk offset of a DATA object
// holding `payload`, appending a new object only when the payload is
// not already present. Duplicate detection walks the bucket chain
// keyed by Hash64(payload) % nBuckets in DATA_HASH_TABLE, comparing
// hash + payload bytes exactly. Callers hold w.mu.
//
// On insert:
//   1. Append a fresh DATA object at TailObjectOffset (hash-chain
//      fields zeroed — next_hash gets patched by the previous tail
//      when we link it into the bucket).
//   2. Update the bucket: if empty, head=tail=new; else patch old
//      tail's next_hash to point to new, then tail=new.
//
// Returns (offset, hash).
func (w *Writer) findOrInsertDataLocked(payload []byte) (uint64, uint64, error) {
	hash := Hash64(payload)
	tableStart := w.header.DataHashTableOffset
	n, err := nBuckets(w.f, tableStart)
	if err != nil {
		return 0, 0, err
	}
	if n == 0 {
		return 0, 0, fmt.Errorf("journalbin: DATA hash table at %d is empty", tableStart)
	}
	bucket := int(hash % uint64(n))
	head, tail, err := readHashItem(w.f, tableStart, bucket)
	if err != nil {
		return 0, 0, err
	}

	// Walk the chain (bounded — pathological hash collisions still
	// terminate at end of chain).
	cur := head
	for cur != 0 {
		curHash, next, curPayload, err := readDataHeader(w.f, cur)
		if err != nil {
			return 0, 0, err
		}
		if curHash == hash && byteEq(curPayload, payload) {
			return cur, hash, nil
		}
		cur = next
	}

	// Not found — append new DATA at tail of arena.
	totalSize := uint64(dataFixedPart + len(payload))
	buf := make([]byte, totalSize)
	dh := ObjectHeader{Type: ObjectData, Size: totalSize}
	if err := dh.EncodeInto(buf); err != nil {
		return 0, 0, err
	}
	binary.LittleEndian.PutUint64(buf[dataHashOffset:dataHashOffset+8], hash)
	// next_hash / next_field / entry_offset / entry_array_offset /
	// n_entries left zero — set later by chain-link / entry-array
	// updates.
	copy(buf[dataPayloadStartOff:], payload)

	dataOffset := w.header.TailObjectOffset
	if _, err := w.f.WriteAt(buf, int64(dataOffset)); err != nil {
		return 0, 0, fmt.Errorf("journalbin: write data at %d: %w", dataOffset, err)
	}
	padded := AlignUp(totalSize)
	if padded > totalSize {
		pad := make([]byte, padded-totalSize)
		if _, err := w.f.WriteAt(pad, int64(dataOffset)+int64(totalSize)); err != nil {
			return 0, 0, fmt.Errorf("journalbin: pad data: %w", err)
		}
	}
	w.header.TailObjectOffset = dataOffset + padded
	w.header.NObjects++
	w.header.NData++

	// Link into bucket. Empty bucket: head=tail=new; otherwise patch
	// old tail's next_hash and bump the bucket tail.
	if head == 0 {
		if err := writeHashItem(w.f, tableStart, bucket, dataOffset, dataOffset); err != nil {
			return 0, 0, err
		}
	} else {
		if err := writeDataNextHash(w.f, tail, dataOffset); err != nil {
			return 0, 0, err
		}
		if err := writeHashItem(w.f, tableStart, bucket, head, dataOffset); err != nil {
			return 0, 0, err
		}
	}
	return dataOffset, hash, nil
}

// appendEntryOffsetLocked adds entryOff to the tail of the
// ENTRY_ARRAY chain. Allocates a new array (capacity 4 initially,
// then doubling up to entryArrayMaxCap) when the current tail is
// full. Updates Header.EntryArrayOffset (first-time), NEntryArrays,
// TailObjectOffset, and ArenaSize.
//
// Caller holds w.mu.
func (w *Writer) appendEntryOffsetLocked(entryOff uint64) error {
	// Allocate a fresh array when: no arrays yet, tail is full, OR
	// a TAG just fenced the current tail (forceNewArray).
	if w.forceNewArray || w.entryArrayTail == 0 || w.entryArrayTailFill >= w.entryArrayTailCap {
		newCap := entryArrayInitialCap
		if w.entryArrayTailCap > 0 {
			newCap = w.entryArrayTailCap * entryArrayGrowthFactor
			if newCap > entryArrayMaxCap {
				newCap = entryArrayMaxCap
			}
		}
		newAt := w.header.TailObjectOffset
		writtenBytes, err := allocateEntryArray(w.f, newAt, newCap)
		if err != nil {
			return err
		}
		w.header.TailObjectOffset = newAt + writtenBytes
		w.header.NObjects++
		w.header.NEntryArrays++

		if w.entryArrayTail == 0 {
			// First array — record in header.
			w.header.EntryArrayOffset = newAt
		} else {
			// Link previous tail to this new one.
			if err := writeEntryArrayNext(w.f, w.entryArrayTail, newAt); err != nil {
				return err
			}
		}
		w.entryArrayTail = newAt
		w.entryArrayTailCap = newCap
		w.entryArrayTailFill = 0
		w.forceNewArray = false
	}

	// Write entry offset into the next free slot.
	if err := writeEntryArraySlot(w.f, w.entryArrayTail, w.entryArrayTailFill, entryOff); err != nil {
		return err
	}
	w.entryArrayTailFill++
	return nil
}

// writeSealTagLocked computes the HMAC over file[fssLastTagEnd..
// TailObjectOffset) with epoch key `e`, then appends a TAG object
// carrying (seqnum, epoch, hmac) at the tail. Updates fssLastTagEnd
// to just past the TAG so the next tag's HMAC starts after it.
//
// Caller holds w.mu and must have already flushed all preceding
// objects to disk (WriteAt does that unconditionally in this
// package — no bufio between us and pwrite).
func (w *Writer) writeSealTagLocked(e int64) error {
	if w.fssKey == nil {
		return nil
	}
	key, err := w.fssKey.DeriveEpochKey(e)
	if err != nil {
		return err
	}
	spanStart := w.fssLastTagEnd
	spanEnd := w.header.TailObjectOffset
	if spanEnd <= spanStart {
		return nil
	}
	// HMAC covers IMMUTABLE bytes only: full ENTRY bodies + DATA
	// payloads (past the mutable next_hash/next_field/entry_offset
	// prefix). Skipping the mutable prefix, hash tables, and
	// entry_arrays preserves the invariant that a written TAG's
	// bytes-under-HMAC never change after the fact — later Appends
	// still patch those regions but they're outside HMAC scope.
	//
	// Verify walks the same iteration deterministically so the two
	// sides produce byte-identical inputs to HMAC.
	digestInput, err := collectHMACInput(w.f, spanStart, spanEnd)
	if err != nil {
		return err
	}
	hmacTag := SealHMACBytes(key, digestInput)

	// Encode TAG object.
	tagBuf := make([]byte, tagObjectSize)
	th := ObjectHeader{Type: ObjectTag, Size: tagObjectSize}
	if err := th.EncodeInto(tagBuf); err != nil {
		return err
	}
	binary.LittleEndian.PutUint64(tagBuf[tagSeqnumOffset:tagSeqnumOffset+8], w.fssNextTagSeqnum)
	binary.LittleEndian.PutUint64(tagBuf[tagEpochOffset:tagEpochOffset+8], uint64(e))
	copy(tagBuf[tagHmacOffset:tagHmacOffset+FSSSealTagSize], hmacTag)

	tagOffset := w.header.TailObjectOffset
	padded := AlignUp(tagObjectSize)
	full := make([]byte, padded)
	copy(full, tagBuf)
	if _, err := w.f.WriteAt(full, int64(tagOffset)); err != nil {
		return fmt.Errorf("journalbin: write tag at %d: %w", tagOffset, err)
	}
	w.header.TailObjectOffset = tagOffset + padded
	w.header.NObjects++
	w.header.NTags++
	w.header.ArenaSize = w.header.TailObjectOffset - HeaderSize
	w.fssLastTagEnd = w.header.TailObjectOffset
	w.fssNextTagSeqnum++
	// Fence the entry-array chain: the sealed span may have included
	// the current tail array's bytes; forcing a fresh array on next
	// Append preserves the "bytes covered by a TAG are immutable"
	// invariant.
	w.forceNewArray = true
	return nil
}

// byteEq is a tiny inline byte-slice equality — avoids the bytes
// package import on the append hot path.
func byteEq(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeHeaderLocked flushes the in-memory Header to offset 0 on disk.
// Caller holds w.mu. Called after every Append so a crash leaves the
// last-written entry recoverable via TailObjectOffset.
func (w *Writer) writeHeaderLocked() error {
	buf := make([]byte, HeaderSize)
	if err := w.header.EncodeInto(buf); err != nil {
		return err
	}
	if _, err := w.f.WriteAt(buf, 0); err != nil {
		return fmt.Errorf("journalbin: write header: %w", err)
	}
	return nil
}

// Flush forces an fsync of the current file. Callers that need
// crash-consistency after N appends (journald's fsync-every-N policy)
// invoke this.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Sync()
}

// Close flushes any pending seal (if FSS is enabled and there are
// un-sealed bytes since the last TAG), flips the header state to
// Archived, fsyncs, and closes the file. After Close further Append
// calls return an error.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	if w.fssKey != nil && w.fssEntriesSinceTag > 0 {
		if err := w.writeSealTagLocked(w.fssLastEpoch); err != nil {
			_ = w.f.Close()
			w.f = nil
			return err
		}
	}
	w.header.State = StateArchived
	if err := w.writeHeaderLocked(); err != nil {
		_ = w.f.Close()
		w.f = nil
		return err
	}
	if err := w.f.Sync(); err != nil {
		_ = w.f.Close()
		w.f = nil
		return err
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// eventFields turns a journal.Event into the "KEY=value" byte slices
// that will land as DATA objects. Field names use uppercase snake_case
// matching systemd's conventions; underscore-prefixed names are
// daemon-stamped identity (systemd's convention is _PID / _UID / …).
// Freeform Fields are appended last, preserving their client-supplied
// keys.
func eventFields(evt *journal.Event) [][]byte {
	// Pre-size for the common case: 5-8 core fields + Fields map.
	fields := make([][]byte, 0, 8+len(evt.Fields))
	appendKV := func(key, value string) {
		if value == "" {
			return
		}
		buf := make([]byte, 0, len(key)+1+len(value))
		buf = append(buf, key...)
		buf = append(buf, '=')
		buf = append(buf, value...)
		fields = append(fields, buf)
	}
	appendKVInt := func(key string, value int) {
		if value == 0 {
			return
		}
		appendKV(key, itoa(value))
	}

	appendKV("MESSAGE", evt.Msg)
	appendKV("PRIORITY", itoa(int(evt.Prio)))
	appendKV("SYSLOG_IDENTIFIER", evt.SyslogIdentifier)
	appendKV("_TRANSPORT", string(evt.Transport))
	appendKV("_SLINIT_UNIT", evt.Unit)
	appendKVInt("_PID", evt.Pid)
	appendKVInt("_UID", evt.Uid)
	appendKVInt("_GID", evt.Gid)
	appendKV("_COMM", evt.Comm)
	appendKV("_EXE", evt.Exe)
	appendKV("_CMDLINE", evt.Cmdline)
	appendKV("_HOSTNAME", evt.Hostname)
	appendKV("_BOOT_ID", evt.BootID)
	appendKV("_MACHINE_ID", evt.MachineID)
	// Freeform client fields last. Order not guaranteed (Go map
	// iteration randomness) — cursor comparisons don't depend on
	// item order within an entry (xor_hash is order-invariant).
	for k, v := range evt.Fields {
		appendKV(k, v)
	}
	return fields
}

// itoa is a tiny fmt.Itoa-equivalent avoiding the fmt package on the
// Append hot path.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// fillUUID fills b with 16 random bytes from crypto/rand. Used for
// FileID and SeqnumID on a fresh journal file.
func fillUUID(b []byte) error {
	if len(b) != 16 {
		return fmt.Errorf("journalbin: UUID buf must be 16 bytes, got %d", len(b))
	}
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("journalbin: rand: %w", err)
	}
	return nil
}

// decodeUUIDHex decodes a 32-lowercase-hex string (systemd format,
// same as journal.BootID output) into a 16-byte destination. Returns
// an error on wrong length or non-hex character — the caller decides
// whether to zero-fill (empty string) or bubble up.
func decodeUUIDHex(hex string, dst []byte) error {
	if len(hex) != 32 {
		return fmt.Errorf("journalbin: UUID hex length %d != 32", len(hex))
	}
	if len(dst) != 16 {
		return fmt.Errorf("journalbin: UUID dst length %d != 16", len(dst))
	}
	for i := 0; i < 16; i++ {
		hi, err := hexNibble(hex[i*2])
		if err != nil {
			return err
		}
		lo, err := hexNibble(hex[i*2+1])
		if err != nil {
			return err
		}
		dst[i] = hi<<4 | lo
	}
	return nil
}

// hexNibble converts a single lowercase-hex char to its 4-bit value.
func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, fmt.Errorf("journalbin: bad hex char %q", c)
	}
}
