package journalbin

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// Reader iterates events from a .journal file in on-disk order (which
// equals arrival order — Writer only appends). Concurrent-safe for
// read: multiple Readers can share an *os.File-backed reader by
// calling NewReader on the same path separately.
//
// v1 does NOT bisect (that's B2b — needs ENTRY_ARRAY chain). Iter
// walks every object linearly, filters to ENTRY, and rebuilds the
// journal.Event from item offsets → DATA payloads.
type Reader struct {
	f      *os.File
	header *Header
}

// OpenReader opens path read-only and validates the header. Returns
// an error if the file is missing, unreadable, has bad magic, or
// claims an unknown incompat flag.
func OpenReader(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("journalbin: open %s: %w", path, err)
	}
	hdrBuf := make([]byte, HeaderSize)
	if _, err := f.ReadAt(hdrBuf, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("journalbin: read header %s: %w", path, err)
	}
	h, err := DecodeHeader(hdrBuf)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("journalbin: header %s: %w", path, err)
	}
	return &Reader{f: f, header: h}, nil
}

// Close releases the underlying file. Idempotent.
func (r *Reader) Close() error {
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Header returns a pointer to the parsed on-disk header. Read-only —
// mutating it does not affect the file.
func (r *Reader) Header() *Header { return r.header }

// EntryOffsets walks the ENTRY_ARRAY chain and returns every entry
// offset in on-disk order (which is arrival order — Writer only
// appends). Empty when the file has no entries. Useful for callers
// that want random access to entries without a linear file scan.
func (r *Reader) EntryOffsets() ([]uint64, error) {
	var out []uint64
	if err := walkEntryOffsets(r.f, r.header.EntryArrayOffset, func(off uint64) bool {
		out = append(out, off)
		return true
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// SeekRealtime returns the offset of the first ENTRY with realtime
// >= targetUsec. Returns (0, false) if no such entry exists (target
// is past the tail). Uses the ENTRY_ARRAY chain built by the Writer,
// which gives O(N) walk + O(log K) bisect within the array holding
// the target. For B2b we implement the straightforward "walk arrays
// until we find one whose head realtime >= target, then bisect that
// array"; upgrading to a bisect across arrays needs per-array
// realtime metadata that systemd doesn't store either.
//
// Callers wanting `--since`/`--until` semantics call SeekRealtime
// once and iterate forward via readEntryAt on each offset.
func (r *Reader) SeekRealtime(targetUsec uint64) (uint64, bool, error) {
	offsets, err := r.EntryOffsets()
	if err != nil {
		return 0, false, err
	}
	if len(offsets) == 0 {
		return 0, false, nil
	}
	// Bisect on realtime by reading each candidate's ENTRY realtime
	// field (offset within entry = ObjectHeaderSize + 8 seqnum = 24).
	lo, hi := 0, len(offsets)
	for lo < hi {
		mid := (lo + hi) / 2
		rt, err := r.entryRealtimeAt(offsets[mid])
		if err != nil {
			return 0, false, err
		}
		if rt < targetUsec {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == len(offsets) {
		return 0, false, nil
	}
	return offsets[lo], true, nil
}

// entryRealtimeAt reads just the realtime microseconds field from
// the ENTRY at `off` without pulling the whole entry / resolving
// items. Used by SeekRealtime for its bisect loop.
func (r *Reader) entryRealtimeAt(off uint64) (uint64, error) {
	// Layout: ObjectHeader(16) + seqnum(8) + realtime(8) at offset 24.
	var buf [8]byte
	if _, err := r.f.ReadAt(buf[:], int64(off)+int64(ObjectHeaderSize+8)); err != nil {
		return 0, fmt.Errorf("journalbin: read entry realtime at %d: %w", off, err)
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}

// ReadEntryAt is the public counterpart to readEntryAt — resolves
// the ENTRY at `off` back into a journal.Event. Used by sd_journal
// API callers that already have offsets from SeekRealtime /
// EntryOffsets and want the full event.
func (r *Reader) ReadEntryAt(off uint64) (*journal.Event, error) {
	var hdrBuf [ObjectHeaderSize]byte
	if _, err := r.f.ReadAt(hdrBuf[:], int64(off)); err != nil {
		return nil, fmt.Errorf("journalbin: read object header at %d: %w", off, err)
	}
	oh, err := DecodeObjectHeader(hdrBuf[:])
	if err != nil {
		return nil, err
	}
	if oh.Type != ObjectEntry {
		return nil, fmt.Errorf("journalbin: expected ENTRY at %d, got %s", off, oh.Type)
	}
	return r.readEntryAt(off, oh.Size)
}

// Iter walks the file linearly and calls fn once per ENTRY object,
// yielding the reconstructed journal.Event. Stops when fn returns
// false or the arena ends. Returns an error on I/O or corruption.
func (r *Reader) Iter(fn func(*journal.Event) bool) error {
	// Bounded by the file size we saw at Open. If a writer is
	// concurrently appending, we simply won't see events past our
	// initial read horizon — the reader semantics match what
	// systemd's sd_journal_next does with a static-file view.
	st, err := r.f.Stat()
	if err != nil {
		return fmt.Errorf("journalbin: stat: %w", err)
	}
	fileSize := uint64(st.Size())

	off := uint64(HeaderSize)
	for off < fileSize {
		hdrBuf := make([]byte, ObjectHeaderSize)
		if _, err := r.f.ReadAt(hdrBuf, int64(off)); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("journalbin: read object header at %d: %w", off, err)
		}
		oh, err := DecodeObjectHeader(hdrBuf)
		if err != nil {
			return fmt.Errorf("journalbin: decode object header at %d: %w", off, err)
		}
		if oh.Size < ObjectHeaderSize {
			return fmt.Errorf("journalbin: object size %d < header size %d at %d",
				oh.Size, ObjectHeaderSize, off)
		}
		// Overflow-safe bounds check. `off + oh.Size` can wrap on a
		// hostile Size near uint64.Max, sneaking past a naive
		// `off+size > fileSize`. Rewriting as `oh.Size > fileSize-off`
		// keeps the arithmetic entirely inside the file-size domain
		// (fileSize-off is guaranteed non-negative because we enter
		// the loop only when off < fileSize).
		if oh.Size > fileSize-off {
			return fmt.Errorf("journalbin: object at %d claims size %d, past file end %d: %w",
				off, oh.Size, fileSize, ErrObjectBounds)
		}
		next := off + AlignUp(oh.Size)
		// Forward-progress guard. AlignUp(oh.Size) can wrap on a huge
		// Size and land next <= off, which would loop the walker
		// forever (fuzz timeout in FuzzJournalBinaryOpenReader
		// surfaced this exact shape). The bounds check above catches
		// the extreme case but not the boundary where AlignUp
		// overflows exactly to (fileSize-off); belt-and-suspenders
		// check ensures the walker always terminates.
		if next <= off {
			return fmt.Errorf("journalbin: object at %d has non-progressing size %d (aligned wrapped)",
				off, oh.Size)
		}

		if oh.Type == ObjectEntry {
			evt, err := r.readEntryAt(off, oh.Size)
			if err != nil {
				return err
			}
			if !fn(evt) {
				return nil
			}
		}
		off = next
	}
	return nil
}

// readEntryAt reads and reconstructs an event from the ENTRY object
// at `off` with declared `size` bytes. Resolves each item's DATA
// offset and re-parses the KEY=value payload back into
// journal.Event fields.
func (r *Reader) readEntryAt(off, size uint64) (*journal.Event, error) {
	if size < ObjectHeaderSize+8+8+8+16+8 {
		return nil, fmt.Errorf("journalbin: entry at %d size %d too small for fixed part", off, size)
	}
	body := make([]byte, size)
	if _, err := r.f.ReadAt(body, int64(off)); err != nil {
		return nil, fmt.Errorf("journalbin: read entry at %d: %w", off, err)
	}
	// Skip common header, then fixed ENTRY fields.
	i := ObjectHeaderSize
	seqnum := binary.LittleEndian.Uint64(body[i : i+8])
	i += 8
	realtimeUsec := binary.LittleEndian.Uint64(body[i : i+8])
	i += 8
	monotonicUsec := binary.LittleEndian.Uint64(body[i : i+8])
	i += 8
	var bootID [16]byte
	copy(bootID[:], body[i:i+16])
	i += 16
	xorHash := binary.LittleEndian.Uint64(body[i : i+8])
	i += 8

	// Items follow: each 16 bytes (offset(8) + hash(8)).
	itemsBytes := body[i:]
	if len(itemsBytes)%16 != 0 {
		return nil, fmt.Errorf("journalbin: entry at %d items region not multiple of 16 (%d bytes)", off, len(itemsBytes))
	}
	nItems := len(itemsBytes) / 16

	evt := &journal.Event{
		Ts:     int64(realtimeUsec) * 1000, // us → ns
		Mts:    int64(monotonicUsec) * 1000,
		BootID: encodeUUIDHex(bootID[:]),
	}
	// Recompute xor_hash for integrity check.
	var xorCheck uint64
	for k := 0; k < nItems; k++ {
		dataOff := binary.LittleEndian.Uint64(itemsBytes[k*16 : k*16+8])
		itemHash := binary.LittleEndian.Uint64(itemsBytes[k*16+8 : k*16+16])
		xorCheck ^= itemHash
		payload, err := r.readDataPayloadAt(dataOff)
		if err != nil {
			return nil, err
		}
		applyKV(evt, payload)
	}
	if xorCheck != xorHash {
		return nil, fmt.Errorf("journalbin: entry at %d xor_hash %#x != items xor %#x: %w",
			off, xorHash, xorCheck, ErrHashMismatch)
	}
	// Seqnum stashed as freeform for cursor round-trip; sd_journal
	// API surfaces it via GetCursor.
	if evt.Fields == nil {
		evt.Fields = map[string]string{}
	}
	evt.Fields["_SEQNUM"] = strconv.FormatUint(seqnum, 10)
	return evt, nil
}

// readDataPayloadAt fetches a DATA object at `off` and returns its
// KEY=value payload bytes (without the leading DATA header + hash
// chain metadata).
func (r *Reader) readDataPayloadAt(off uint64) ([]byte, error) {
	hdrBuf := make([]byte, ObjectHeaderSize)
	if _, err := r.f.ReadAt(hdrBuf, int64(off)); err != nil {
		return nil, fmt.Errorf("journalbin: read data header at %d: %w", off, err)
	}
	oh, err := DecodeObjectHeader(hdrBuf)
	if err != nil {
		return nil, err
	}
	if oh.Type != ObjectData {
		return nil, fmt.Errorf("journalbin: expected DATA at %d, got %s", off, oh.Type)
	}
	const dataFixed = ObjectHeaderSize + 8 + 8 + 8 + 8 + 8 + 8
	if oh.Size < dataFixed {
		return nil, fmt.Errorf("journalbin: DATA at %d size %d < fixed %d", off, oh.Size, dataFixed)
	}
	payloadLen := oh.Size - dataFixed
	if payloadLen == 0 {
		return nil, nil
	}
	payload := make([]byte, payloadLen)
	if _, err := r.f.ReadAt(payload, int64(off)+int64(dataFixed)); err != nil {
		return nil, fmt.Errorf("journalbin: read data payload at %d: %w", off, err)
	}
	return payload, nil
}

// applyKV takes a "KEY=value" byte slice from a DATA payload and
// writes it back into the correct journal.Event field. Unknown keys
// land in evt.Fields.
func applyKV(evt *journal.Event, kv []byte) {
	eq := -1
	for i, b := range kv {
		if b == '=' {
			eq = i
			break
		}
	}
	if eq < 0 {
		// Malformed — surface as freeform.
		if evt.Fields == nil {
			evt.Fields = map[string]string{}
		}
		evt.Fields[string(kv)] = ""
		return
	}
	key := string(kv[:eq])
	value := string(kv[eq+1:])
	switch key {
	case "MESSAGE":
		evt.Msg = value
	case "PRIORITY":
		if p, err := strconv.Atoi(value); err == nil {
			evt.Prio = journal.Priority(p)
		}
	case "SYSLOG_IDENTIFIER":
		evt.SyslogIdentifier = value
	case "_TRANSPORT":
		evt.Transport = journal.Transport(value)
	case "_SLINIT_UNIT":
		evt.Unit = value
	case "_PID":
		if n, err := strconv.Atoi(value); err == nil {
			evt.Pid = n
		}
	case "_UID":
		if n, err := strconv.Atoi(value); err == nil {
			evt.Uid = n
		}
	case "_GID":
		if n, err := strconv.Atoi(value); err == nil {
			evt.Gid = n
		}
	case "_COMM":
		evt.Comm = value
	case "_EXE":
		evt.Exe = value
	case "_CMDLINE":
		evt.Cmdline = value
	case "_HOSTNAME":
		evt.Hostname = value
	case "_BOOT_ID":
		evt.BootID = value
	case "_MACHINE_ID":
		evt.MachineID = value
	default:
		if evt.Fields == nil {
			evt.Fields = map[string]string{}
		}
		evt.Fields[key] = value
	}
}

// encodeUUIDHex is the inverse of decodeUUIDHex — 16 bytes to 32
// lowercase hex chars. Used to render the boot_id back onto
// journal.Event.BootID so consumers see the same wire format as
// Phase 1.
func encodeUUIDHex(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	var sb strings.Builder
	sb.Grow(32)
	const hexdigits = "0123456789abcdef"
	for _, x := range b {
		sb.WriteByte(hexdigits[x>>4])
		sb.WriteByte(hexdigits[x&0x0f])
	}
	return sb.String()
}
