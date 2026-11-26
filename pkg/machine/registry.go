// Package machine implements slinit's flat-file container registry.
//
// A "machine" here is any process tree that runs slinit (or another
// init) as PID 1 inside its own PID namespace. slinit does not ship a
// D-Bus machined equivalent — the registry is a directory of files
// under /run/slinit/machines/, one file per registered container,
// holding the container's host-visible PID 1. This matches the
// OpenRC/runit style of "flat files instead of a management daemon"
// (dinit philosophy: optional daemon).
//
// Consumers use pkg/machine to:
//   - Register a container (slinit-nspawn writes the file after fork)
//   - Look up a container's host PID by name (slinit-journalctl -M)
//   - List registered containers (slinit-machinectl list)
//
// The registry is host-scoped — machines registered on host H are only
// visible to H's own tools. Cross-host inspection is out of scope.
package machine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultDir is the on-disk root for machine registry files. Runtime
// state, so /run — not /var. Kept public so tests can override via
// SetDir; production callers rarely need to.
const DefaultDir = "/run/slinit/machines"

var currentDir = DefaultDir

// SetDir overrides the registry directory. Intended for tests + tools
// that need to isolate per-user or per-scope registries (e.g. a rootless
// runner keeping its own tree under $XDG_RUNTIME_DIR/slinit/machines).
// Not concurrency-safe — set at process startup.
func SetDir(dir string) { currentDir = dir }

// Dir returns the registry directory currently in effect.
func Dir() string { return currentDir }

// Machine describes one registered container as viewed from the host.
type Machine struct {
	// Name is the label the operator chose (e.g. "web", "alpine-1").
	// One name per file; a re-register with the same name overwrites.
	Name string
	// PID is the host-visible PID of the container's PID 1 (usually
	// the slinit process inside the container's PID namespace).
	PID int
	// Class is a free-form tag: "container", "vm", "namespace",
	// "chroot" — informational, not enforced. Optional.
	Class string
	// Service is the slinit service that manages the container's
	// lifetime, if any. Empty when the container was launched
	// manually. Informational.
	Service string
	// Root is the filesystem root of the container as seen from the
	// host (a bind-mount source, chroot path, or /). Used to resolve
	// on-disk journal files when the socket dial fails. Empty is OK
	// and means "use /proc/PID/root".
	Root string
	// StartTime is the registered process's start time, in clock ticks
	// since boot (field 22 of /proc/PID/stat). With the PID it names
	// the process: an entry outlives its container when the launcher
	// is SIGKILLed, and the PID can then belong to anything. Zero means
	// not recorded, as in entries written before it was; see State.
	StartTime uint64

	// registered is the entry file's mtime: when it was written. It
	// stands in for StartTime on entries that lack one.
	registered time.Time
}

// State is what a registry entry's PID refers to now.
type State int

const (
	// StateDead: no process has the PID.
	StateDead State = iota
	// StateAlive: the registered process is still running.
	StateAlive
	// StateReused: a process has the PID, but not the registered one —
	// it started after the entry was written.
	StateReused
	// StateUnknown: a process has the PID, but its identity cannot be
	// checked (its /proc entry is hidden, e.g. mounted hidepid=).
	StateUnknown
)

// String is the word slinit-machinectl prints for the state.
func (s State) String() string {
	switch s {
	case StateAlive:
		return "yes"
	case StateReused:
		return "reused"
	case StateUnknown:
		return "unknown"
	}
	return "no"
}

// procDir is /proc; tests point it elsewhere.
var procDir = "/proc"

// userHZ is the unit of /proc/PID/stat's starttime (USER_HZ, which the
// kernel fixes at 100 for user space on every architecture Go targets
// except alpha, and Go does not target alpha).
const userHZ = 100

// legacySlack is how much later than its entry was written a process
// may appear to have started and still count as the registered one,
// when the entry has no StartTime. Boot time is reported in whole
// seconds and moves with clock adjustments.
const legacySlack = 2 * time.Second

// ProcStartTime returns pid's start time in clock ticks since boot,
// field 22 of /proc/PID/stat. The fields are counted after the last
// ')': the command name before it may itself hold spaces and ')'.
func ProcStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	return parseStartTime(string(data))
}

func parseStartTime(stat string) (uint64, error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("machine: malformed /proc/PID/stat")
	}
	// After ')' the fields start at 3 (state); starttime is 22.
	f := strings.Fields(stat[i+1:])
	if len(f) < 20 {
		return 0, errors.New("machine: short /proc/PID/stat")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// bootTime returns the btime line of /proc/stat.
func bootTime() (time.Time, error) {
	data, err := os.ReadFile(filepath.Join(procDir, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			return time.Unix(n, 0), nil
		}
	}
	return time.Time{}, errors.New("machine: no btime in /proc/stat")
}

// State reports whether the entry's PID still names the registered
// process. With a StartTime that is an exact match. An entry written
// before StartTime was recorded is checked against the time it was
// written instead: a running container registered by an older version
// stays alive, while a process that started after the entry was written
// (more than legacySlack later) cannot be the one it names.
func (m *Machine) State() State {
	if m.PID <= 1 {
		return StateDead
	}
	st, err := ProcStartTime(m.PID)
	if err != nil {
		if Alive(m.PID) {
			return StateUnknown
		}
		return StateDead
	}
	if m.StartTime != 0 {
		if st == m.StartTime {
			return StateAlive
		}
		return StateReused
	}
	if m.registered.IsZero() {
		return StateUnknown
	}
	boot, err := bootTime()
	if err != nil {
		return StateUnknown
	}
	started := boot.Add(time.Duration(st) * time.Second / userHZ)
	if started.After(m.registered.Add(legacySlack)) {
		return StateReused
	}
	return StateAlive
}

// validName rejects registry keys that would let a caller escape the
// registry directory or collide with hidden files.
func validName(name string) error {
	if name == "" {
		return errors.New("machine: empty name")
	}
	if len(name) > 64 {
		return fmt.Errorf("machine: name %q exceeds 64 bytes", name)
	}
	if name[0] == '.' || name[0] == '-' {
		return fmt.Errorf("machine: name %q must not start with . or -", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.'
		if !ok {
			return fmt.Errorf("machine: name %q contains invalid character %q", name, c)
		}
	}
	return nil
}

// validField rejects a metadata value that would not survive the
// line-oriented file format: any ASCII control character (newline,
// carriage return, NUL, escape...) or DEL.
func validField(key, val string) error {
	for i := 0; i < len(val); i++ {
		if c := val[i]; c < 0x20 || c == 0x7f {
			return fmt.Errorf("machine: %s %q contains control character %q", key, val, c)
		}
	}
	return nil
}

// path returns the registry file for `name` after validation.
func path(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	return filepath.Join(currentDir, name), nil
}

// Register writes a new registry entry. Overwrites atomically via
// temp-then-rename so a partial write cannot surface. The directory
// is created on demand with 0755.
func Register(m Machine) error {
	if err := validName(m.Name); err != nil {
		return err
	}
	if m.PID <= 1 {
		return fmt.Errorf("machine: PID %d is not a valid container PID", m.PID)
	}
	// Each field is one line of the file: a newline in --class would
	// start a line of its own, e.g. a forged ROOT= that -M then trusts.
	for _, f := range []struct{ key, val string }{
		{"class", m.Class}, {"service", m.Service}, {"root", m.Root},
	} {
		if err := validField(f.key, f.val); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		return fmt.Errorf("machine: mkdir %s: %w", currentDir, err)
	}
	final, err := path(m.Name)
	if err != nil {
		return err
	}
	if m.StartTime == 0 {
		// Best effort: a PID that cannot be read (gone, or hidden
		// from this user) is registered as before, and State checks
		// it against the time of registration.
		if st, err := ProcStartTime(m.PID); err == nil {
			m.StartTime = st
		}
	}
	// Format: line 1 = PID, line 2 = CLASS=<class>, line 3 = SERVICE=<service>,
	// line 4 = ROOT=<root>, line 5 = STARTTIME=<ticks>. Missing lines
	// default to empty. Keeping
	// key=value keeps this human-inspectable via `cat` and forward-
	// compatible with new fields.
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", m.PID)
	if m.Class != "" {
		fmt.Fprintf(&b, "CLASS=%s\n", m.Class)
	}
	if m.Service != "" {
		fmt.Fprintf(&b, "SERVICE=%s\n", m.Service)
	}
	if m.Root != "" {
		fmt.Fprintf(&b, "ROOT=%s\n", m.Root)
	}
	if m.StartTime != 0 {
		fmt.Fprintf(&b, "STARTTIME=%d\n", m.StartTime)
	}
	// The temp file's name starts with '.', which no machine name may,
	// so it can neither collide with an entry nor be listed as one: a
	// fixed final+".tmp" was itself a valid name, and registering "a"
	// replaced machine "a.tmp" while List hid it.
	f, err := os.CreateTemp(currentDir, "."+m.Name+".*")
	if err != nil {
		return fmt.Errorf("machine: create temp in %s: %w", currentDir, err)
	}
	tmp := f.Name()
	_, err = f.WriteString(b.String())
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("machine: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("machine: rename %s: %w", final, err)
	}
	return nil
}

// Unregister deletes a registry entry. Missing entry is not an error —
// the caller wanted the entry gone and it's gone.
func Unregister(name string) error {
	p, err := path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("machine: remove %s: %w", p, err)
	}
	return nil
}

// Lookup returns the registered Machine for `name`. Returns
// (nil, nil) when the name is unknown so callers can distinguish
// "not registered" from "registry unreadable".
func Lookup(name string) (*Machine, error) {
	p, err := path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("machine: read %s: %w", p, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("machine: stat %s: %w", p, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("machine: read %s: %w", p, err)
	}
	m := &Machine{Name: name, registered: fi.ModTime()}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if i == 0 {
			pid, perr := strconv.Atoi(strings.TrimSpace(line))
			if perr != nil {
				return nil, fmt.Errorf("machine: %s: line 1 not a PID: %w", p, perr)
			}
			m.PID = pid
			continue
		}
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "CLASS":
			m.Class = val
		case "SERVICE":
			m.Service = val
		case "ROOT":
			m.Root = val
		case "STARTTIME":
			st, perr := strconv.ParseUint(val, 10, 64)
			if perr != nil {
				return nil, fmt.Errorf("machine: %s: STARTTIME: %w", p, perr)
			}
			m.StartTime = st
		}
	}
	return m, nil
}

// List returns every registered machine, one entry per file under the
// registry directory. Stale entries (PID gone or reused) are still
// returned — filtering is the caller's job (Machine.State).
func List() ([]*Machine, error) {
	entries, err := os.ReadDir(currentDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("machine: readdir %s: %w", currentDir, err)
	}
	var out []*Machine
	for _, e := range entries {
		// Dotfiles are Register's in-flight temp files; no machine
		// name starts with '.'.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		m, err := Lookup(e.Name())
		if err != nil {
			// Skip corrupted entries; a bad file shouldn't hide the
			// rest. slinit-machinectl surfaces a WARN separately.
			continue
		}
		if m != nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// Alive returns true when the PID is still a running process — some
// process; whether it is still the registered one is Machine.State's
// question, and the one to ask about a registry entry. Uses
// kill(pid, 0) — the standard "is this PID reachable" probe on Linux.
// EPERM means the process exists but belongs to someone the caller may
// not signal: a container started by root, seen by an unprivileged
// `slinit-machinectl list`. Only ESRCH means it is gone.
// Not race-free against a PID that dies mid-check, but good enough
// for `slinit-machinectl list` and stale-entry pruning.
func Alive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
