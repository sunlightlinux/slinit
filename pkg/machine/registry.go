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
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		return fmt.Errorf("machine: mkdir %s: %w", currentDir, err)
	}
	final, err := path(m.Name)
	if err != nil {
		return err
	}
	// Format: line 1 = PID, line 2 = CLASS=<class>, line 3 = SERVICE=<service>,
	// line 4 = ROOT=<root>. Missing lines default to empty. Keeping
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
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
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
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("machine: read %s: %w", p, err)
	}
	m := &Machine{Name: name}
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
		}
	}
	return m, nil
}

// List returns every registered machine, one entry per file under the
// registry directory. Stale entries (PID no longer alive) are still
// returned — filtering is the caller's job (Alive helper below).
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
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
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

// Alive returns true when the PID is still a running process. Uses
// kill(pid, 0) — the standard "is this PID reachable" probe on Linux.
// Not race-free against a PID that dies mid-check, but good enough
// for `slinit-machinectl list` and stale-entry pruning.
func Alive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}
