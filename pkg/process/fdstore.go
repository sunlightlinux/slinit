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
// understands (READY, STATUS, FDSTORE, FDNAME, WATCHDOG, STOPPING).
// Unknown keys are ignored.
type NotifyMessage struct {
	Ready    bool
	Stopping bool
	FDStore  bool
	FDName   string
	Watchdog bool
	Status   string
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

