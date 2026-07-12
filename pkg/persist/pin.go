// Package persist implements durable operator-intent storage —
// currently pin state, potentially wider "activation intent" later.
//
// Rationale: slinit's in-memory activation flags are lost on every
// daemon restart / reboot. For most services that matches the operator
// mental model ("I started it once at boot, restart brings it back
// via the boot graph"), but a manually pinned-stopped service should
// STAY down across reboots too — otherwise the operator has to run
// `stop --pin` again on every boot.
//
// Format: one file per service under a configured directory
// (typically /var/lib/slinit/intent). File name equals the service
// name. Contents is one line, one of:
//
//	pinned-started
//	pinned-stopped
//
// Absent file means "no persistent intent for this service" — its
// activation follows the normal boot graph.
//
// Errors are logged but non-fatal: a full disk or read-only /var must
// not stop the daemon from booting, just from PERSISTING intents
// until the underlying issue is fixed.
package persist

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Intent values recorded on disk. Keep as untyped strings so
// forward-compat additions (activated, deactivated, …) don't require
// a wire-format bump.
const (
	IntentPinnedStarted = "pinned-started"
	IntentPinnedStopped = "pinned-stopped"
)

// PinStore is the disk-backed pin-intent persistence layer. A zero
// value with dir=="" is a valid no-op store — the daemon uses that
// when `--persist-intent` isn't set, so every hook site can call the
// same methods unconditionally.
type PinStore struct {
	mu  sync.Mutex
	dir string
}

// NewPinStore creates a store rooted at dir. Empty dir returns a
// no-op store. The directory is created lazily on the first write
// so a fresh install doesn't need a preseed.
func NewPinStore(dir string) *PinStore {
	return &PinStore{dir: dir}
}

// Enabled reports whether persistence is active — cheap check used
// to short-circuit at call sites that would otherwise do work only
// needed when writing.
func (p *PinStore) Enabled() bool { return p != nil && p.dir != "" }

// Set writes intent for the given service, replacing any prior file.
// intent must be one of the IntentPinned* constants; any other value
// is treated as a programming error and rejected.
func (p *PinStore) Set(name, intent string) error {
	if !p.Enabled() {
		return nil
	}
	switch intent {
	case IntentPinnedStarted, IntentPinnedStopped:
	default:
		return fmt.Errorf("persist: unknown intent %q", intent)
	}
	if err := validName(name); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(p.dir, 0755); err != nil {
		return fmt.Errorf("persist: mkdir %s: %w", p.dir, err)
	}
	// Atomic write: temp file + rename so a crash mid-write can't
	// leave a truncated intent that would be misparsed at boot.
	path := filepath.Join(p.dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(intent+"\n"), 0644); err != nil {
		return fmt.Errorf("persist: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("persist: rename %s: %w", tmp, err)
	}
	return nil
}

// Clear removes the intent file for the given service. Missing file
// is not an error — the caller reaches Clear via `unpin` / `release`,
// both of which are idempotent from the operator's perspective.
func (p *PinStore) Clear(name string) error {
	if !p.Enabled() {
		return nil
	}
	if err := validName(name); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	err := os.Remove(filepath.Join(p.dir, name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("persist: remove %s: %w", name, err)
	}
	return nil
}

// Load returns every recorded intent as a name→intent map. Callers
// use this at boot to replay pins BEFORE the boot graph starts
// cascading — pinning a service stopped after it's already been
// started via a dep is legal but noisier than pinning before.
//
// Files with a corrupted or unknown intent value are logged (via the
// returned error, best-effort) and skipped, so a broken file for one
// service can't gate the whole restore.
func (p *PinStore) Load() (map[string]string, error) {
	if !p.Enabled() {
		return nil, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]string)
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("persist: read %s: %w", p.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		name := e.Name()
		if err := validName(name); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.dir, name))
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(data))
		switch v {
		case IntentPinnedStarted, IntentPinnedStopped:
			out[name] = v
		default:
			// Silently ignore corrupt file — a future field-audit
			// can grep the daemon log for the read errors that
			// preceded this state.
		}
	}
	return out, nil
}

// validName rejects service names that could escape the persistence
// dir (path traversal) or otherwise land the store in a wedged state.
// The daemon's own ValidateServiceName is stricter but importing it
// would cycle; the checks below cover the concrete attack surface.
func validName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("persist: invalid service name %q", name)
	}
	if strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("persist: invalid service name %q", name)
	}
	return nil
}
