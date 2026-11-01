package process

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
)

// FDStoreEntry is one file descriptor preserved across a service
// restart. systemd's sd_notify protocol associates each fd with a
// human name (FDNAME=foo) so the service can find specific listening
// sockets after re-exec.
type FDStoreEntry struct {
	Name string
	File *os.File
}

// FDStore holds the file descriptors a service has stashed via the
// sd_notify FDSTORE=1 protocol. The store is per-service and lives
// in slinit's memory only — daemon restart loses everything, just
// like systemd.
//
// All operations are mutex-protected because the receiver runs in
// its own goroutine.
type FDStore struct {
	mu      sync.Mutex
	max     int
	entries []FDStoreEntry
}

// NewFDStore creates an empty store with the given capacity (0 means
// disabled — no fds will be accepted).
func NewFDStore(max int) *FDStore {
	return &FDStore{max: max}
}

// Add inserts an entry. When the store is full or max == 0 the new
// fd is closed and ErrFDStoreFull is returned so the receiver does
// not leak a descriptor on rejection.
func (s *FDStore) Add(e FDStoreEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.max <= 0 {
		if e.File != nil {
			e.File.Close()
		}
		return fmt.Errorf("fd-store disabled (max=0)")
	}
	if len(s.entries) >= s.max {
		if e.File != nil {
			e.File.Close()
		}
		return fmt.Errorf("fd-store full (max=%d)", s.max)
	}
	s.entries = append(s.entries, e)
	return nil
}

// Remove closes and drops every entry whose name match accepts, and
// returns how many it dropped. Backs FDSTOREREMOVE=1 (exact name) and
// FDSTOREWIPE= (glob).
func (s *FDStore) Remove(match func(name string) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.entries[:0]
	n := 0
	for _, e := range s.entries {
		if !match(e.Name) {
			kept = append(kept, e)
			continue
		}
		if e.File != nil {
			e.File.Close()
		}
		n++
	}
	clear(s.entries[len(kept):]) // no stale *os.File left in the backing array
	s.entries = kept
	return n
}

// ValidFDName reports whether name may name a stored fd, or (as a
// pattern) select them: 1 to 255 printable ASCII characters, no ':'.
// systemd's rule for FDNAME= and FDSTOREWIPE= alike.
func ValidFDName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c > 0x7e || c == ':' {
			return false
		}
	}
	return true
}

// MatchFDName reports whether name matches pattern as fnmatch(3) does
// with no flags, which is how systemd applies FDSTOREWIPE=: '*' and '?'
// also match '/' and a leading '.', "[...]" is a set ('!' or '^'
// negates), and a backslash makes the next character literal.
//
// The pattern comes from a service and is matched in PID 1, so this is
// the linear-time form: every element but '*' consumes exactly one
// character, so only the last '*' needs a resume point, and the work is
// bounded by len(pattern) * len(name). A recursive '*' is exponential
// on "*a*a*a...b".
func MatchFDName(pattern, name string) bool {
	p, n := 0, 0
	starP, starN := -1, 0
	for n < len(name) {
		if p < len(pattern) {
			if pattern[p] == '*' {
				starP, starN = p, n
				p++
				continue
			}
			if next, ok := matchFDNameOne(pattern, p, name[n]); ok {
				p, n = next, n+1
				continue
			}
		}
		if starP < 0 {
			return false
		}
		// Let the last '*' take one more character and retry after it.
		starN++
		p, n = starP+1, starN
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchFDNameOne matches the single-character element at pattern[p]
// (anything but '*') against c, returning the index past the element.
func matchFDNameOne(pattern string, p int, c byte) (int, bool) {
	switch pattern[p] {
	case '?':
		return p + 1, true
	case '[':
		rest, in, closed := matchFDNameSet(pattern[p+1:], c)
		if !closed {
			// No closing ']': fnmatch takes the '[' literally.
			return p + 1, c == '['
		}
		return len(pattern) - len(rest), in
	case '\\':
		if p+1 < len(pattern) {
			return p + 2, pattern[p+1] == c
		}
	}
	return p + 1, pattern[p] == c
}

// matchFDNameSet matches c against the set that starts at p (just past
// the '['): the pattern after the closing ']', whether c is in the set,
// and whether there was a closing ']' at all. A ']' first in the set is
// a member, not the end.
func matchFDNameSet(p string, c byte) (rest string, in, closed bool) {
	negate := false
	if p != "" && (p[0] == '!' || p[0] == '^') {
		negate, p = true, p[1:]
	}
	for i := 0; i < len(p); i++ {
		if p[i] == ']' && i > 0 {
			return p[i+1:], in != negate, true
		}
		lo := p[i]
		if lo == '\\' && i+1 < len(p) {
			i++
			lo = p[i]
		}
		hi := lo
		if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
			hi = p[i+2]
			if hi == '\\' && i+3 < len(p) {
				i++
				hi = p[i+2]
			}
			i += 2
		}
		if lo <= c && c <= hi {
			in = true
		}
	}
	return "", false, false
}

// Drain returns the current entries and resets the store. Used by
// StartProcess so each restart hands out the stashed fds exactly
// once — re-storing must come from the new child's sd_notify.
func (s *FDStore) Drain() []FDStoreEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.entries
	s.entries = nil
	return out
}

// Len reports the current entry count (for tests / introspection).
func (s *FDStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Max reports the configured capacity.
func (s *FDStore) Max() int { return s.max }

// Close closes every stored file. Called when the service is unloaded
// or the daemon is shutting down — otherwise the fds leak.
func (s *FDStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.File != nil {
			e.File.Close()
		}
	}
	s.entries = nil
}

// ParseNotifyMessage decodes one sd_notify packet body. systemd's
// wire format is one VAR=VAL per line; we extract the subset slinit
// understands (READY, STATUS, FDSTORE, FDSTOREREMOVE, FDSTOREWIPE,
// FDNAME, WATCHDOG, STOPPING). Unknown keys are ignored.
type NotifyMessage struct {
	Ready         bool
	Stopping      bool
	FDStore       bool
	FDStoreRemove bool
	// FDStoreWipe is the FDSTOREWIPE= glob; HasFDStoreWipe tells an
	// empty one (invalid, and ignored) from none at all.
	FDStoreWipe    string
	HasFDStoreWipe bool
	FDName         string
	Watchdog       bool
	Status         string
}

// ParseNotifyMessage parses the text portion of a sd_notify datagram.
// The fds (if any) come from the SCM_RIGHTS cmsg; this function only
// looks at the body.
func ParseNotifyMessage(body []byte) NotifyMessage {
	var m NotifyMessage
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		key, val, ok := bytes.Cut(line, []byte{'='})
		if !ok {
			continue
		}
		switch strings.ToUpper(string(key)) {
		case "READY":
			m.Ready = string(val) == "1"
		case "STOPPING":
			m.Stopping = string(val) == "1"
		case "FDSTORE":
			m.FDStore = string(val) == "1"
		case "FDSTOREREMOVE":
			m.FDStoreRemove = string(val) == "1"
		case "FDSTOREWIPE":
			m.FDStoreWipe, m.HasFDStoreWipe = string(val), true
		case "FDNAME":
			m.FDName = string(val)
		case "WATCHDOG":
			m.Watchdog = string(val) == "1"
		case "STATUS":
			m.Status = string(val)
		}
	}
	return m
}
