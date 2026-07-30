package journal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
)

// idHexLen is the 128-bit ID encoding used across systemd + slinit:
// 32 lowercase hex characters, no dashes. Matches sd_id128_t on-disk
// representation exactly, so /etc/machine-id written by systemd is
// consumable by slinit without translation and vice versa.
const idHexLen = 32

// BootIDPath is where slinit persists the current-boot ID. Lives on
// tmpfs so it disappears at reboot — that's how journalctl detects
// boot rollover. If /run is not writable (unusual for PID 1 but
// possible in containers), InitBootID falls back to an in-memory ID
// with a warning.
const BootIDPath = "/run/slinit/boot-id"

// MachineIDPath is the standard systemd machine-id location. slinit
// prefers reading an existing file over generating one to keep the
// invariant that a host's machine_id stays stable across init-system
// swaps (systemd → slinit and back).
const MachineIDPath = "/etc/machine-id"

// idCache holds the resolved IDs after Init. Both are set once at
// startup and never mutated, so plain values (no atomic) suffice
// under the guard of initOnce.
var (
	idCache struct {
		mu        sync.RWMutex
		boot      string
		machine   string
		hostname  string
		initDone  bool
	}
)

// InitIDs resolves BootID, MachineID, and Hostname exactly once,
// caching them for the lifetime of the process. slinit calls this at
// PID-1 startup from cmd/slinit/main.go so every event afterwards can
// stamp them via the cheap getters below.
//
// The runtime dir at /run/slinit is created if missing. Failure to
// write BootID is non-fatal: emit stamps whatever InitIDs returned,
// even if it never touched disk.
func InitIDs(hostname string) error {
	idCache.mu.Lock()
	defer idCache.mu.Unlock()

	if idCache.initDone {
		return nil
	}

	boot, err := resolveBootID()
	if err != nil {
		return fmt.Errorf("journal: resolve boot ID: %w", err)
	}
	idCache.boot = boot

	machine, err := resolveMachineID()
	if err != nil {
		return fmt.Errorf("journal: resolve machine ID: %w", err)
	}
	idCache.machine = machine

	idCache.hostname = hostname
	idCache.initDone = true
	return nil
}

// BootID returns the current-boot 128-bit hex identifier. Panics if
// InitIDs has not been called — a programming error at PID 1 setup,
// not a runtime condition that emit code should handle.
func BootID() string {
	idCache.mu.RLock()
	defer idCache.mu.RUnlock()
	if !idCache.initDone {
		panic("journal: BootID() called before InitIDs — cmd/slinit must call InitIDs early in boot")
	}
	return idCache.boot
}

// MachineID returns the persistent host identity.
func MachineID() string {
	idCache.mu.RLock()
	defer idCache.mu.RUnlock()
	if !idCache.initDone {
		panic("journal: MachineID() called before InitIDs")
	}
	return idCache.machine
}

// Hostname returns the resolved hostname (uname -n) captured at
// InitIDs time. slinit-journalctl output prefixes lines with this in
// "short" mode.
func Hostname() string {
	idCache.mu.RLock()
	defer idCache.mu.RUnlock()
	if !idCache.initDone {
		panic("journal: Hostname() called before InitIDs")
	}
	return idCache.hostname
}

// resolveBootID first tries to read an existing BootIDPath (in case
// slinit was restarted in-place via soft-reboot or exec-reload
// without a real reboot); if absent, generates a fresh 128-bit ID
// from crypto/rand and writes it to disk best-effort.
func resolveBootID() (string, error) {
	if existing, err := readIDFile(BootIDPath); err == nil {
		return existing, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		// I/O error on an existing file is worth surfacing.
		return "", fmt.Errorf("read %s: %w", BootIDPath, err)
	}

	// Generate.
	id, err := newRandomID()
	if err != nil {
		return "", err
	}

	// Best-effort persist; log-and-ignore the error path is fine
	// because in-memory ID still keeps the current boot consistent.
	if err := os.MkdirAll("/run/slinit", 0o755); err == nil {
		// 0o644 so anyone reading (slinit-journalctl running as
		// non-root) can see the current boot.
		if err := writeIDFile(BootIDPath, id); err != nil {
			// Don't fail InitIDs on this — the in-memory ID is
			// still valid for the current boot. Log via stderr
			// so at least it's visible in the catch-all logger.
			fmt.Fprintf(os.Stderr, "journal: warning: cannot persist boot-id to %s: %v\n",
				BootIDPath, err)
		}
	}
	return id, nil
}

// resolveMachineID reads /etc/machine-id if present. On first-boot
// systems (or container images that skipped machine-id-setup) the
// file is missing; we generate a random ID but do NOT persist to
// /etc/machine-id — that's the job of a first-boot service or
// slinit-init-maker. Leaving persistence to a dedicated setup path
// keeps InitIDs read-only w.r.t. /etc and avoids surprises for
// operators who rely on the file being written by their own tooling.
func resolveMachineID() (string, error) {
	if existing, err := readIDFile(MachineIDPath); err == nil {
		return existing, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", MachineIDPath, err)
	}

	// Fall back to a transient one. Warn but don't fail.
	fmt.Fprintf(os.Stderr,
		"journal: warning: %s missing; using transient machine ID (see slinit-init-maker to fix)\n",
		MachineIDPath)
	return newRandomID()
}

// readIDFile parses a 32-hex-char ID file (systemd machine-id format).
// Trailing whitespace is tolerated. Anything else is treated as
// malformed and rejected — a partial write corrupts the ID space
// silently otherwise.
func readIDFile(path string) (string, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(buf))
	if !isValidID(s) {
		return "", fmt.Errorf("%s: malformed ID %q (want 32 lowercase hex chars)", path, s)
	}
	return s, nil
}

// writeIDFile writes the ID atomically via rename-in-place so a crash
// mid-write never leaves a truncated file that would fail isValidID
// on the next boot's read.
func writeIDFile(path, id string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newRandomID generates a fresh 128-bit ID and encodes it as 32
// lowercase hex chars. crypto/rand.Read is documented to never fail
// on Linux (backed by getrandom(2)); the error path returns the raw
// error unwrapped so the caller can surface it cleanly.
func newRandomID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// isValidID checks that s matches the sd_id128_t canonical form:
// exactly 32 lowercase hex chars. Uppercase and dashes are rejected
// — systemd + slinit + shell tools all agree on the lowercase-no-
// dashes form, so we don't accept variants that would round-trip
// inconsistently.
func isValidID(s string) bool {
	if len(s) != idHexLen {
		return false
	}
	for i := 0; i < idHexLen; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// resetIDsForTest resets the cache so a test can call InitIDs again
// with different inputs. Exposed only within the package's test
// binary; production code never calls it.
func resetIDsForTest() {
	idCache.mu.Lock()
	idCache.boot = ""
	idCache.machine = ""
	idCache.hostname = ""
	idCache.initDone = false
	idCache.mu.Unlock()
}
