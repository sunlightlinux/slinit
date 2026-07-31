package journald

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// IdxRecordSize is the on-disk size of one index entry: two little-
// endian uint64s. First 8 bytes are the event's wall-clock timestamp
// in MICROseconds since Unix epoch (systemd cursor convention — us,
// not ns; keeps records under 20 chars for grep-readable dumps).
// Second 8 bytes are the byte offset of the entry's start in the
// paired JSONL file.
const IdxRecordSize = 16

// IdxRecord is the parsed form of one 16-byte index entry.
type IdxRecord struct {
	TsUsec int64
	Offset int64
}

// idxSuffix is appended to a jsonl file name to derive its companion.
// "<name>.jsonl.idx" makes both files visibly related and keeps the
// original extension recognizable (a stray tool that inspects .jsonl
// files still sees the right suffix).
const idxSuffix = ".idx"

// idxPath returns the companion index path for a given JSONL path.
// Split out so tests can assert the convention and future rotation
// code has a single place to update if we ever change the scheme.
func idxPath(jsonlPath string) string {
	return jsonlPath + idxSuffix
}

// encodeIdxRecord writes one 16-byte record into b. b must be at
// least IdxRecordSize bytes. Returns the slice header advanced by
// the write, matching bytes.Writer.Write conventions.
func encodeIdxRecord(b []byte, r IdxRecord) []byte {
	binary.LittleEndian.PutUint64(b[0:8], uint64(r.TsUsec))
	binary.LittleEndian.PutUint64(b[8:16], uint64(r.Offset))
	return b[IdxRecordSize:]
}

// decodeIdxRecord parses one 16-byte record from b. b must be at
// least IdxRecordSize bytes; short buffers return zero record and
// an error.
func decodeIdxRecord(b []byte) (IdxRecord, error) {
	if len(b) < IdxRecordSize {
		return IdxRecord{}, fmt.Errorf("journald: idx record short: %d < %d", len(b), IdxRecordSize)
	}
	return IdxRecord{
		TsUsec: int64(binary.LittleEndian.Uint64(b[0:8])),
		Offset: int64(binary.LittleEndian.Uint64(b[8:16])),
	}, nil
}

// IdxReader provides bisect lookup on an existing .idx file. Opens
// the file read-only, memory-maps nothing (we stat() for size and
// pread each 16-byte record), so multiple concurrent readers can
// bisect the same file without a mutex.
type IdxReader struct {
	f       *os.File
	records int64 // size/16
}

// OpenIdx opens path for read. Returns error if the file is missing,
// unreadable, or a non-multiple of IdxRecordSize (corruption
// signal — the writer only ever appends whole records, so a partial
// tail record means an interrupted write worth surfacing).
func OpenIdx(path string) (*IdxReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	sz := st.Size()
	if sz%IdxRecordSize != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("journald: idx %s is not a multiple of %d bytes (size %d)",
			path, IdxRecordSize, sz)
	}
	return &IdxReader{f: f, records: sz / IdxRecordSize}, nil
}

// Close releases the underlying file. Idempotent.
func (r *IdxReader) Close() error {
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Len returns the number of records in the index.
func (r *IdxReader) Len() int64 { return r.records }

// At reads record i (0-indexed). Bounds-checked; returns an error
// for out-of-range.
func (r *IdxReader) At(i int64) (IdxRecord, error) {
	if i < 0 || i >= r.records {
		return IdxRecord{}, fmt.Errorf("journald: idx index %d out of range [0,%d)", i, r.records)
	}
	var buf [IdxRecordSize]byte
	if _, err := r.f.ReadAt(buf[:], i*IdxRecordSize); err != nil {
		return IdxRecord{}, err
	}
	return decodeIdxRecord(buf[:])
}

// LowerBound returns the smallest index i such that record[i].TsUsec
// >= tsUsec. If no such record exists, returns Len(). Callers pass
// the returned offset to os.File.Seek on the paired JSONL to start
// reading from the matching entry.
//
// Classic binary bisect against a monotonically-increasing key —
// entries are always written in wall-clock arrival order, so
// records[i].TsUsec is non-decreasing.
func (r *IdxReader) LowerBound(tsUsec int64) (int64, error) {
	lo, hi := int64(0), r.records
	for lo < hi {
		mid := (lo + hi) / 2
		rec, err := r.At(mid)
		if err != nil {
			return 0, err
		}
		if rec.TsUsec < tsUsec {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

// RebuildIdx scans a JSONL file line by line and rewrites its
// companion .idx from scratch. Called by tools that detect a stale
// or corrupt index (size mismatch, decode failure), or manually by
// operators after crash recovery.
//
// Reader is stateful; on entry it should be positioned at the start
// of the file. Returns the number of records written.
func RebuildIdx(jsonlPath string) (int, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	out, err := os.Create(idxPath(jsonlPath))
	if err != nil {
		return 0, err
	}
	defer out.Close()

	// We can't use bufio.Scanner cleanly because we need the exact
	// byte offset of each line's start, not just the line contents.
	// Read into a growing buffer and slice on newline manually.
	const chunk = 64 * 1024
	buf := make([]byte, 0, chunk)
	readBuf := make([]byte, chunk)
	offset := int64(0)
	lineStart := int64(0)
	count := 0
	writeBuf := make([]byte, IdxRecordSize)

	for {
		n, err := f.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
			for {
				nl := indexByte(buf, '\n')
				if nl < 0 {
					break
				}
				line := buf[:nl]
				if len(line) > 0 {
					ts, perr := peekTsUsec(line)
					if perr == nil {
						encodeIdxRecord(writeBuf, IdxRecord{TsUsec: ts, Offset: lineStart})
						if _, werr := out.Write(writeBuf); werr != nil {
							return count, werr
						}
						count++
					}
				}
				advance := int64(nl + 1)
				buf = buf[advance:]
				lineStart += advance
			}
			offset += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, err
		}
	}
	if err := out.Sync(); err != nil {
		return count, err
	}
	return count, nil
}

// indexByte is a tiny bytes.IndexByte reimplementation kept inline
// so the rebuild loop avoids the import surface and stays trivially
// auditable.
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// peekTsUsec extracts the "ts" field from a JSONL line as
// microseconds without a full json.Unmarshal — the rebuild path
// runs over potentially GiB of data and full unmarshalling would
// dwarf the disk-read cost.
//
// We scan for `"ts":<number>` and parse the digits directly. Returns
// an error if the field is missing or malformed; the caller then
// skips that line.
func peekTsUsec(line []byte) (int64, error) {
	// Look for `"ts":`
	needle := []byte(`"ts":`)
	i := 0
	for ; i+len(needle) <= len(line); i++ {
		match := true
		for k := 0; k < len(needle); k++ {
			if line[i+k] != needle[k] {
				match = false
				break
			}
		}
		if match {
			i += len(needle)
			break
		}
	}
	if i >= len(line) {
		return 0, errors.New("journald: no ts field")
	}
	// Skip optional whitespace after the colon.
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	// Parse a signed integer.
	sign := int64(1)
	if i < len(line) && line[i] == '-' {
		sign = -1
		i++
	}
	if i >= len(line) || line[i] < '0' || line[i] > '9' {
		return 0, errors.New("journald: ts is not numeric")
	}
	var v int64
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		v = v*10 + int64(line[i]-'0')
		i++
	}
	// ts is Unix NANOSECONDS on the wire; the idx stores microseconds.
	return sign * v / 1000, nil
}
