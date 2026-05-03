package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultPath is where snapshots live when no override is supplied.
// Same parent as the boot-time clock guard so distros only need to
// provision /var/lib/slinit once.
const DefaultPath = "/var/lib/slinit/snapshot.json"

// Read parses a snapshot file from disk.
//
// Returns an error wrapping os.ErrNotExist if the file is missing —
// callers can treat that as "no prior snapshot, fresh boot" without
// having to import errors.Is themselves at the boundary.
//
// A snapshot whose Version is newer than CurrentVersion is rejected:
// the daemon refuses to silently misinterpret a file written by a
// future binary. Older versions are read as-is — that is the whole
// point of additive named fields.
func Read(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("snapshot %s: parse: %w", path, err)
	}

	if snap.Version > CurrentVersion {
		return nil, fmt.Errorf("snapshot %s: version %d is newer than %d",
			path, snap.Version, CurrentVersion)
	}

	return &snap, nil
}

// Write atomically persists snap to path.
//
// The write is durable across crashes: marshal → write to a sibling
// `.tmp` file → fsync → rename. After a power cut the operator sees
// either the old snapshot or the new one, never a half-written file.
//
// The parent directory is created with 0o755 if missing; the snapshot
// itself is written 0o600 because it can include process arguments and
// environment variables that the operator may consider sensitive.
func Write(path string, snap *Snapshot) error {
	if snap.Version == 0 {
		snap.Version = CurrentVersion
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("snapshot marshal: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("snapshot mkdir %s: %w", dir, err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("snapshot create %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("snapshot write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("snapshot fsync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("snapshot close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("snapshot rename %s: %w", path, err)
	}
	return nil
}
