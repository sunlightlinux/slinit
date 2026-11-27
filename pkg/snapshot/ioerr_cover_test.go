package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

// A Write that cannot finish leaves the previous snapshot as it was and
// no temporary file behind.
func TestWriteFailureKeepsOldSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	old := &Snapshot{Services: []ServiceSnapshot{{Name: "old", Activated: true}}}
	if err := Write(path, old); err != nil {
		t.Fatal(err)
	}

	// The temp name is taken by a directory: the create fails.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, &Snapshot{Services: []ServiceSnapshot{{Name: "new"}}}); err == nil {
		t.Fatal("Write succeeded with its temp path taken by a directory")
	}
	got, err := Read(path)
	if err != nil || len(got.Services) != 1 || got.Services[0].Name != "old" {
		t.Fatalf("old snapshot not intact after a failed Write: %+v, %v", got, err)
	}
}

// The rename onto a directory fails; the temp file is removed.
func TestWriteRenameFailureRemovesTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, &Snapshot{}); err == nil {
		t.Fatal("Write succeeded onto a non-empty directory")
	}
	if _, err := os.Lstat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp file left behind after a failed rename: %v", err)
	}
}

// A parent that is a regular file cannot be created as a directory.
func TestWriteParentIsFile(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "file")
	if err := os.WriteFile(parent, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(parent, "snapshot.json"), &Snapshot{}); err == nil {
		t.Fatal("Write succeeded under a regular file")
	}
}

// A snapshot that is not JSON is an error, not an empty snapshot that
// would restore nothing without saying so.
func TestReadCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte("{\"version\": 1, \"services\": [ trunc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("Read accepted a truncated snapshot")
	}
}
