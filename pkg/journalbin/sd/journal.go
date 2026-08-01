// Package sd implements a Go, semantic-compat subset of the
// libsystemd-journal API. Method names map onto sd_journal_* function
// names (documented per method), and behaviour matches sd_journal
// closely enough that operators moving from journalctl to
// slinit-journalctl see the expected results — but this is NOT ABI
// compat: no C symbols exported, no cgo, no library linking.
//
// The API surface for v1 covers what slinit-journalctl needs to
// implement `--follow` / `--file` / `--since` / `--cursor`
// (open/close, next/previous, get_data, seek_realtime_usec,
// get/test cursor, add_match). Extension is intentionally trivial —
// add a new method here + wire it into slinit-journalctl. FSS
// verify is exposed via the top-level journalbin.Verify function,
// not through this API.
package sd

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/journal"
	"github.com/sunlightlinux/slinit/pkg/journalbin"
)

// Journal is the equivalent of sd_journal handle. It merges any
// number of *.journal files (opened via OpenFiles or OpenDirectory)
// into one time-ordered stream, tracks a current position, and
// answers Get* calls for the entry at that position.
//
// Not safe for concurrent use by multiple goroutines — matches
// sd_journal's own thread-safety guarantee (each sd_journal handle
// belongs to one thread).
type Journal struct {
	// entries is the global, time-sorted array across all files.
	// Rebuilt lazily on first access after Add/Remove file, so
	// callers pay the sort cost once per topology change rather
	// than per Next/Previous.
	entries []mergedEntry
	// pos is the index of the current entry (-1 = before first,
	// len(entries) = past last).
	pos int
	// readers keyed by file path — Close closes each one.
	readers []*journalbin.Reader
	// matches is the AND-of-ORs match filter (sd_journal_add_match
	// semantics). Empty means no filter.
	matches []matchGroup
}

// mergedEntry is a compact reference into a specific reader. Sort
// order is realtime asc (ties by reader index asc so ordering is
// deterministic across runs).
type mergedEntry struct {
	realtime  uint64
	readerIdx int
	offset    uint64
}

// matchGroup collects OR-combined matches for one field. Multiple
// groups are AND'd together per sd_journal semantics: `-u a -u b -p err`
// = (UNIT=a OR UNIT=b) AND (PRIORITY<=err).
type matchGroup struct {
	field    string
	acceptFn func(value string) bool
	// acceptRaw is used only for stringly-equal matches, so
	// TestMatches can compare against the original spec.
	acceptRaw []string
}

// Open constructs a Journal that reads a directory of .journal files
// (equivalent to sd_journal_open("dir")). Non-directory paths return
// an error — use OpenFiles for a specific file set.
func Open(dir string) (*Journal, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("sd: open %s: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("sd: open %s: not a directory (use OpenFiles for a single file)", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("sd: read dir %s: %w", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".journal") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	return OpenFiles(paths...)
}

// OpenFiles constructs a Journal that reads exactly the listed files
// (equivalent to sd_journal_open_files). Empty list returns a Journal
// with no data — Next() will immediately return false.
func OpenFiles(paths ...string) (*Journal, error) {
	j := &Journal{pos: -1}
	for _, p := range paths {
		r, err := journalbin.OpenReader(p)
		if err != nil {
			j.Close()
			return nil, err
		}
		j.readers = append(j.readers, r)
	}
	if err := j.rebuildIndex(); err != nil {
		j.Close()
		return nil, err
	}
	return j, nil
}

// Close releases all sub-readers. Idempotent (sd_journal_close).
func (j *Journal) Close() error {
	var firstErr error
	for _, r := range j.readers {
		if err := r.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	j.readers = nil
	j.entries = nil
	return firstErr
}

// rebuildIndex walks every reader, gathers entry offsets + realtimes,
// and sorts. Called after Open* and after any AddMatch/FlushMatches
// (though matches don't change offsets — they're filtered at Get*
// time — so calls after AddMatch just refresh the internal cursor).
func (j *Journal) rebuildIndex() error {
	var all []mergedEntry
	for ri, r := range j.readers {
		offs, err := r.EntryOffsets()
		if err != nil {
			return err
		}
		for _, off := range offs {
			evt, err := r.ReadEntryAt(off)
			if err != nil {
				return err
			}
			all = append(all, mergedEntry{
				realtime:  uint64(evt.Ts / 1000),
				readerIdx: ri,
				offset:    off,
			})
		}
	}
	sort.SliceStable(all, func(i, k int) bool {
		if all[i].realtime != all[k].realtime {
			return all[i].realtime < all[k].realtime
		}
		return all[i].readerIdx < all[k].readerIdx
	})
	j.entries = all
	j.pos = -1
	return nil
}

// Next advances to the next matching entry (sd_journal_next). Returns
// true if positioned on a valid entry, false at EOF.
func (j *Journal) Next() bool {
	for {
		if j.pos+1 >= len(j.entries) {
			j.pos = len(j.entries)
			return false
		}
		j.pos++
		if j.currentMatches() {
			return true
		}
	}
}

// Previous moves backward (sd_journal_previous). Returns true if
// positioned on a valid entry, false at BOF.
func (j *Journal) Previous() bool {
	for {
		if j.pos <= 0 {
			j.pos = -1
			return false
		}
		j.pos--
		if j.pos < 0 {
			return false
		}
		if j.currentMatches() {
			return true
		}
	}
}

// SeekHead positions before the first entry so Next() lands on the
// first (sd_journal_seek_head). Never errors.
func (j *Journal) SeekHead() error {
	j.pos = -1
	return nil
}

// SeekTail positions past the last entry so Previous() lands on it
// (sd_journal_seek_tail). Never errors.
func (j *Journal) SeekTail() error {
	j.pos = len(j.entries)
	return nil
}

// SeekRealtimeUsec positions on the first entry whose realtime is >=
// usec (sd_journal_seek_realtime_usec). Follow with Next() to consume.
func (j *Journal) SeekRealtimeUsec(usec int64) error {
	target := uint64(usec)
	// Binary search — entries sorted by realtime.
	idx := sort.Search(len(j.entries), func(i int) bool {
		return j.entries[i].realtime >= target
	})
	// Position "before" the found entry so Next() steps onto it.
	j.pos = idx - 1
	return nil
}

// Current returns the *journal.Event at the current position. Errors
// if the cursor is invalid (before-first or past-last).
func (j *Journal) Current() (*journal.Event, error) {
	if j.pos < 0 || j.pos >= len(j.entries) {
		return nil, errors.New("sd: cursor not positioned on a valid entry (call Next/Previous first)")
	}
	me := j.entries[j.pos]
	return j.readers[me.readerIdx].ReadEntryAt(me.offset)
}

// GetData returns the value bytes of a single field on the current
// entry (sd_journal_get_data — the field name is stripped, only the
// value returned). Empty and error when the field is absent.
func (j *Journal) GetData(field string) (string, error) {
	evt, err := j.Current()
	if err != nil {
		return "", err
	}
	switch strings.ToUpper(field) {
	case "MESSAGE":
		return evt.Msg, nil
	case "PRIORITY":
		return strconv.Itoa(int(evt.Prio)), nil
	case "SYSLOG_IDENTIFIER":
		return evt.SyslogIdentifier, nil
	case "_TRANSPORT":
		return string(evt.Transport), nil
	case "_SLINIT_UNIT":
		return evt.Unit, nil
	case "_PID":
		if evt.Pid == 0 {
			return "", fmt.Errorf("sd: field %q absent on current entry", field)
		}
		return strconv.Itoa(evt.Pid), nil
	case "_UID":
		return strconv.Itoa(evt.Uid), nil
	case "_GID":
		return strconv.Itoa(evt.Gid), nil
	case "_COMM":
		return evt.Comm, nil
	case "_EXE":
		return evt.Exe, nil
	case "_CMDLINE":
		return evt.Cmdline, nil
	case "_HOSTNAME":
		return evt.Hostname, nil
	case "_BOOT_ID":
		return evt.BootID, nil
	case "_MACHINE_ID":
		return evt.MachineID, nil
	}
	if v, ok := evt.Fields[field]; ok {
		return v, nil
	}
	return "", fmt.Errorf("sd: field %q absent on current entry", field)
}

// GetRealtimeUsec returns the current entry's wall-clock time in
// microseconds since Unix epoch (sd_journal_get_realtime_usec).
func (j *Journal) GetRealtimeUsec() (int64, error) {
	if j.pos < 0 || j.pos >= len(j.entries) {
		return 0, errors.New("sd: cursor not positioned")
	}
	return int64(j.entries[j.pos].realtime), nil
}

// GetMonotonicUsec returns the current entry's monotonic timestamp
// (sd_journal_get_monotonic_usec). Read from the entry itself since
// the merged index only tracks realtime.
func (j *Journal) GetMonotonicUsec() (int64, error) {
	evt, err := j.Current()
	if err != nil {
		return 0, err
	}
	return evt.Mts / 1000, nil
}

// GetCursor returns an opaque cursor string that can be passed to
// SeekCursor to restore the current position across process
// restarts (sd_journal_get_cursor).
//
// Format: `s=<file_id_hex>;i=<offset>;t=<realtime_usec>`. Superset
// of Phase 2h's simple `s=<ts>;b=<boot>` — SeekCursor accepts both.
// The `i=` component is the byte offset within the chosen file,
// pinning the exact entry unambiguously.
func (j *Journal) GetCursor() (string, error) {
	if j.pos < 0 || j.pos >= len(j.entries) {
		return "", errors.New("sd: cursor not positioned")
	}
	me := j.entries[j.pos]
	r := j.readers[me.readerIdx]
	fid := hex.EncodeToString(r.Header().FileID[:])
	return fmt.Sprintf("s=%s;i=%d;t=%d", fid, me.offset, me.realtime), nil
}

// TestCursor reports whether the given cursor matches the current
// entry (sd_journal_test_cursor). Cheap because it doesn't need to
// re-seek.
func (j *Journal) TestCursor(cursor string) bool {
	cur, err := j.GetCursor()
	if err != nil {
		return false
	}
	return cur == cursor
}

// SeekCursor positions on the entry named by cursor
// (sd_journal_seek_cursor). Accepts both the extended v2 cursor
// (with file_id + offset + realtime) and the Phase 2h cursor
// (`s=<ts>;b=<boot>`); the latter degrades to a realtime seek.
func (j *Journal) SeekCursor(cursor string) error {
	parts := strings.Split(cursor, ";")
	var fileID, offsetStr, realtimeStr string
	var v1TS int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, "s="):
			// Ambiguous: v1 stored ns timestamp here, v2 stores hex
			// file ID. Distinguish by content — 32 hex chars = v2.
			body := p[2:]
			if len(body) == 32 && looksLikeHex(body) {
				fileID = body
			} else if n, err := strconv.ParseInt(body, 10, 64); err == nil {
				v1TS = n
			}
		case strings.HasPrefix(p, "i="):
			offsetStr = p[2:]
		case strings.HasPrefix(p, "t="):
			realtimeStr = p[2:]
		case strings.HasPrefix(p, "b="):
			// v1 boot id — informational only in v2 (each Reader
			// already carries its own BootID via the file header).
		}
	}

	// v2 path: file_id + offset → direct index lookup.
	if fileID != "" && offsetStr != "" {
		target, err := hex.DecodeString(fileID)
		if err != nil || len(target) != 16 {
			return fmt.Errorf("sd: cursor: bad file_id %q", fileID)
		}
		off, err := strconv.ParseUint(offsetStr, 10, 64)
		if err != nil {
			return fmt.Errorf("sd: cursor: bad offset %q", offsetStr)
		}
		for i, me := range j.entries {
			r := j.readers[me.readerIdx]
			if bytesEq(r.Header().FileID[:], target) && me.offset == off {
				j.pos = i - 1 // so next Next() lands on it
				return nil
			}
		}
		// Not found — fall through to realtime as a hint if present.
		if realtimeStr != "" {
			if t, err := strconv.ParseInt(realtimeStr, 10, 64); err == nil {
				return j.SeekRealtimeUsec(t)
			}
		}
		return fmt.Errorf("sd: cursor: entry not found (file_id/offset absent from open files)")
	}

	// v1 fallback: just realtime seek.
	if v1TS > 0 {
		return j.SeekRealtimeUsec(v1TS / 1000) // v1 ts was ns
	}
	return fmt.Errorf("sd: cursor: unparseable %q", cursor)
}

// AddMatch adds a filter of the form "FIELD=value"
// (sd_journal_add_match). Repeated calls with the same field OR
// their values; different fields AND together. Priority values
// like `PRIORITY=3` filter to <=3 (systemd behavior).
func (j *Journal) AddMatch(match string) error {
	eq := strings.IndexByte(match, '=')
	if eq < 0 {
		return fmt.Errorf("sd: AddMatch: %q missing '='", match)
	}
	field := match[:eq]
	value := match[eq+1:]
	// Priority is special — match values are treated as an upper bound.
	if field == "PRIORITY" {
		p, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("sd: AddMatch: priority %q not numeric", value)
		}
		j.matches = append(j.matches, matchGroup{
			field: field,
			acceptFn: func(v string) bool {
				n, err := strconv.Atoi(v)
				if err != nil {
					return false
				}
				return n <= p
			},
			acceptRaw: []string{value},
		})
		return nil
	}
	// Merge into an existing group with the same field (OR-set), or
	// create a fresh group.
	for i, g := range j.matches {
		if g.field == field {
			raw := append(g.acceptRaw, value)
			set := make(map[string]struct{}, len(raw))
			for _, v := range raw {
				set[v] = struct{}{}
			}
			j.matches[i].acceptFn = func(v string) bool {
				_, ok := set[v]
				return ok
			}
			j.matches[i].acceptRaw = raw
			return nil
		}
	}
	set := map[string]struct{}{value: {}}
	j.matches = append(j.matches, matchGroup{
		field: field,
		acceptFn: func(v string) bool {
			_, ok := set[v]
			return ok
		},
		acceptRaw: []string{value},
	})
	return nil
}

// FlushMatches drops every previously added match (sd_journal_flush_matches).
func (j *Journal) FlushMatches() { j.matches = nil }

// currentMatches evaluates every match group against the current
// entry. Empty match set trivially passes.
func (j *Journal) currentMatches() bool {
	if len(j.matches) == 0 {
		return true
	}
	if j.pos < 0 || j.pos >= len(j.entries) {
		return false
	}
	evt, err := j.Current()
	if err != nil {
		return false
	}
	for _, g := range j.matches {
		v, err := getFieldValue(evt, g.field)
		if err != nil {
			return false
		}
		if !g.acceptFn(v) {
			return false
		}
	}
	return true
}

// getFieldValue is a mini-GetData that doesn't reload the event —
// used inside the match-eval loop where we already have the *event.
func getFieldValue(evt *journal.Event, field string) (string, error) {
	switch strings.ToUpper(field) {
	case "MESSAGE":
		return evt.Msg, nil
	case "PRIORITY":
		return strconv.Itoa(int(evt.Prio)), nil
	case "SYSLOG_IDENTIFIER":
		return evt.SyslogIdentifier, nil
	case "_TRANSPORT":
		return string(evt.Transport), nil
	case "_SLINIT_UNIT":
		return evt.Unit, nil
	case "_HOSTNAME":
		return evt.Hostname, nil
	case "_BOOT_ID":
		return evt.BootID, nil
	}
	if v, ok := evt.Fields[field]; ok {
		return v, nil
	}
	return "", fmt.Errorf("field %q absent", field)
}

// looksLikeHex checks if every byte in s is a lowercase hex digit.
func looksLikeHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// bytesEq is a tiny inline byte-slice equality (unrelated to
// pkg/journalbin's own — kept local to avoid a cross-package
// export just for this).
func bytesEq(a, b []byte) bool {
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

// Underscore prefix rule for JOURNAL_FIELD is enforced via
// underlying journalbin's IsValidFieldName during writes; readers
// like this one are permissive so old fields don't break new
// tooling.
