package machine

import (
	stderrors "errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ControlSockPath is the in-container path to slinit's control
// socket — the stream socket slinitctl / slinit-journalctl dial for
// both one-shot queries and --follow subscribes. Same relative path
// as on the host (slinit itself binds it during PID-1 setup, no
// separate daemon required).
const ControlSockPath = "/run/slinit.socket"

// EventsSockPath is the in-container path to the DGRAM event bus
// slinit-journald binds and receives events from. Only present when
// the optional slinit-journald daemon is running inside the
// container. slinit-journalctl doesn't dial this directly — it goes
// through the control socket — but keeping the constant lets
// slinit-machinectl and inspection tools surface the daemon's
// presence.
const EventsSockPath = "/run/slinit/events.sock"

// JournalDirPaths are the in-container paths the persistent journal
// daemon writes to. Persistent first, volatile fallback.
var JournalDirPaths = []string{
	"/var/log/slinit-journal",
	"/run/slinit-journal",
}

// hostRoot renders a container-relative path as seen from the host,
// preferring an explicit Root (bind-mount source) over the
// /proc/PID/root magic link. /proc/PID/root works for any process in
// the same namespace hierarchy as the host, so it's the reliable
// fallback when the container was launched without a known rootfs
// bind-mount source.
func (m *Machine) hostRoot() string {
	if m.Root != "" {
		return m.Root
	}
	return fmt.Sprintf("/proc/%d/root", m.PID)
}

// DialJournal connects to the container's event bus socket, returning
// a net.Conn ready for the same protocol slinit-journalctl uses on the
// host. Returns an error rendered against the resolved host path so
// operators can debug "does the file exist" without guessing.
func (m *Machine) DialJournal() (net.Conn, error) {
	sock := filepath.Join(m.hostRoot(), EventsSockPath)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("machine %s: dial %s: %w", m.Name, sock, err)
	}
	return conn, nil
}

// ListJournalFiles enumerates on-disk JSONL journal files visible
// from the host under the container's rootfs. Ordered by mtime
// (oldest first) so callers can iterate historically. Returns nil
// (not an error) when no directory exists — a container that never
// enabled slinit-journald has no persistent journal, and that's a
// valid state.
func (m *Machine) ListJournalFiles() ([]string, error) {
	root := m.hostRoot()
	var files []string
	for _, rel := range JournalDirPaths {
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if stderrors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("machine %s: readdir %s: %w", m.Name, dir, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".jsonl") &&
				!strings.HasSuffix(name, ".jsonl.gz") &&
				!strings.HasSuffix(name, ".slj") {
				continue
			}
			files = append(files, filepath.Join(dir, name))
		}
	}
	// Stable oldest-first by name — journald rotates with
	// YYYY-MM-DD.jsonl-style filenames so lex order == chrono.
	sort.Strings(files)
	return files, nil
}

