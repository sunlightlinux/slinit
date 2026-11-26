package machine

import (
	stderrors "errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/internal/safepath"
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

// fdPath is the path that reaches what fd refers to.
func fdPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }

// socketIn opens the socket at rel inside root, resolved there and not
// on the host (safepath.OpenInRoot): a container's /run/slinit.socket
// -> /run/slinit.socket otherwise named the host's control socket, and
// -M showed the host's journal as the container's. The socket is
// reached through the returned O_PATH descriptor's /proc/self/fd path.
func socketIn(root, rel string) (int, error) {
	fd, err := safepath.OpenInRoot(root, rel, unix.O_PATH)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		unix.Close(fd)
		return -1, &os.PathError{Op: "open", Path: filepath.Join(root, rel), Err: unix.ENOTSOCK}
	}
	return fd, nil
}

// ControlSocketPath returns a path that reaches the container's control
// socket, resolved inside the container through /proc/PID/root: its
// /run is a tmpfs private to it, which m.Root does not show. The path
// goes through a descriptor kept open for the life of the process.
func (m *Machine) ControlSocketPath() (string, error) {
	fd, err := socketIn(fmt.Sprintf("/proc/%d/root", m.PID), ControlSockPath)
	if err != nil {
		return "", fmt.Errorf("machine %s: %w", m.Name, err)
	}
	return fdPath(fd), nil
}

// DialJournal connects to the container's event bus socket, returning
// a net.Conn ready for the same protocol slinit-journalctl uses on the
// host. Returns an error rendered against the resolved host path so
// operators can debug "does the file exist" without guessing.
func (m *Machine) DialJournal() (net.Conn, error) {
	sock := filepath.Join(m.hostRoot(), EventsSockPath)
	fd, err := socketIn(m.hostRoot(), EventsSockPath)
	if err != nil {
		return nil, fmt.Errorf("machine %s: dial %s: %w", m.Name, sock, err)
	}
	defer unix.Close(fd)
	conn, err := net.Dial("unix", fdPath(fd))
	if err != nil {
		return nil, fmt.Errorf("machine %s: dial %s: %w", m.Name, sock, err)
	}
	return conn, nil
}

// ListJournalFiles enumerates on-disk journal files (JSONL and binary)
// visible from the host under the container's rootfs. Ordered by mtime
// (oldest first) so callers can iterate historically. Returns nil
// (not an error) when no directory exists — a container that never
// enabled slinit-journald has no persistent journal, and that's a
// valid state.
//
// The directories are resolved inside the container's root, not at
// root+path on the host, which follows the container's symlinks out
// of it; only regular files count, a link being able to name a host
// file. The paths go through a descriptor of each directory, kept open
// for the life of the process.
func (m *Machine) ListJournalFiles() ([]string, error) {
	root := m.hostRoot()
	var files []string
	key := map[string]string{} // each path's host spelling, which orders them
	for _, rel := range JournalDirPaths {
		fd, err := safepath.OpenInRoot(root, rel, unix.O_RDONLY|unix.O_DIRECTORY)
		if err != nil {
			if stderrors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("machine %s: readdir %s: %w", m.Name, filepath.Join(root, rel), err)
		}
		dir := fdPath(fd)
		entries, err := os.ReadDir(dir)
		if err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("machine %s: readdir %s: %w", m.Name, filepath.Join(root, rel), err)
		}
		n := len(files)
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".jsonl") &&
				!strings.HasSuffix(name, ".jsonl.gz") &&
				!strings.HasSuffix(name, ".slj") &&
				!strings.HasSuffix(name, ".journal") {
				continue
			}
			files = append(files, filepath.Join(dir, name))
			key[files[len(files)-1]] = filepath.Join(root, rel, name)
		}
		if len(files) == n {
			unix.Close(fd)
		}
	}
	// Stable oldest-first by name — journald rotates with
	// YYYY-MM-DD.jsonl-style filenames so lex order == chrono.
	sort.Slice(files, func(i, j int) bool { return key[files[i]] < key[files[j]] })
	return files, nil
}
