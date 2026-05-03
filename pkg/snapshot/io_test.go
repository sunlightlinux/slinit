package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	want := &Snapshot{
		Version:   CurrentVersion,
		WrittenAt: "2026-05-03T12:00:00Z",
		Services: []ServiceSnapshot{
			{Name: "nginx", Activated: true},
			{Name: "postgres", Activated: true, PinnedStart: true},
			{Name: "cron", Triggered: true},
		},
		GlobalEnv: []string{"TZ=UTC", "LANG=C.UTF-8"},
	}
	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestWriteFillsVersionWhenZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	snap := &Snapshot{} // Version=0
	if err := Write(path, snap); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if snap.Version != CurrentVersion {
		t.Errorf("Write should set Version on the input struct: got %d, want %d",
			snap.Version, CurrentVersion)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Version != CurrentVersion {
		t.Errorf("on-disk Version=%d, want %d", got.Version, CurrentVersion)
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestReadRejectsFutureVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	data := []byte(`{"version": 9999, "services": []}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := Read(path)
	if err == nil {
		t.Fatal("expected error on future-version snapshot")
	}
}

func TestWriteIsAtomic(t *testing.T) {
	// After Write succeeds there is no .tmp file lingering.
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	if err := Write(path, &Snapshot{Version: CurrentVersion}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected .tmp gone after rename, got err=%v", err)
	}
}

func TestWriteCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deeper", "nested", "snapshot.json")
	if err := Write(path, &Snapshot{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected snapshot file at %s, err=%v", path, err)
	}
}

func TestSnapshotPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	if err := Write(path, &Snapshot{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Snapshot may contain env vars an operator considers sensitive.
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("snapshot mode = %#o, want 0o600", mode)
	}
}

// TestAdditiveSchemaUnknownFieldsTolerated: a snapshot written by a
// future Phase B daemon (with extra "pid" / "exec_stage" fields) must
// be readable by Phase A — we just ignore the unknown fields.
func TestAdditiveSchemaUnknownFieldsTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	data := []byte(`{
  "version": 1,
  "services": [
    {"name": "nginx", "activated": true, "pid": 1234, "exec_stage": 7, "future_field": "ignored"}
  ]
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "nginx" || !got.Services[0].Activated {
		t.Errorf("unexpected: %#v", got.Services)
	}
}

// TestAdditiveSchemaMissingFieldsAreZero: a Phase B reader of a
// Phase A snapshot must see zero values for the new fields. Since
// Phase A has no new fields yet, this checks the more general
// invariant: fields absent from JSON deserialize as zero.
func TestAdditiveSchemaMissingFieldsAreZero(t *testing.T) {
	data := []byte(`{"version":1,"services":[{"name":"nginx"}]}`)
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e := snap.Services[0]
	if e.Activated || e.PinnedStart || e.PinnedStop || e.Triggered {
		t.Errorf("expected all flags zero, got %#v", e)
	}
}
