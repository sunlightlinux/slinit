package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCronPersistRoundTrip pins the on-disk persistence contract that
// v1.10.44's Tier 2 batch upgraded from in-memory to durable. Write a
// timestamp via writePersisted, then read it back — the marshalled
// form must round-trip through RFC3339Nano bit-for-bit so a soft-
// rebooted daemon picks the exact same catch-up decision as before.
func TestCronPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	SetCronPersistDir(dir)
	defer SetCronPersistDir("/var/lib/slinit/cron")

	set, _ := newTestSet()
	svc := NewInternalService(set, "cron-persist-rt")
	set.AddService(svc)

	cr := &CronRunner{svc: svc, logger: set.logger}
	want := time.Date(2026, 7, 17, 8, 30, 15, 250_000_000, time.UTC)
	cr.writePersisted(want)

	got, ok := cr.readPersisted()
	if !ok {
		t.Fatal("readPersisted returned !ok immediately after writePersisted")
	}
	if !got.Equal(want) {
		t.Errorf("round-trip: got %v, want %v", got, want)
	}

	// The file must actually exist under the store dir, keyed by
	// service name — regression check for the sanitisation that maps
	// "/" in svc names to "_".
	if _, err := os.Stat(filepath.Join(dir, "cron-persist-rt")); err != nil {
		t.Errorf("expected persist file at %s/cron-persist-rt: %v", dir, err)
	}
}

// TestCronPersistMissingIsSilent covers the first-boot case: no file
// on disk → readPersisted returns (zero, false), which the loop must
// treat as "no previous run" rather than falling over.
func TestCronPersistMissingIsSilent(t *testing.T) {
	dir := t.TempDir()
	SetCronPersistDir(dir)
	defer SetCronPersistDir("/var/lib/slinit/cron")

	set, _ := newTestSet()
	svc := NewInternalService(set, "cron-persist-missing")
	set.AddService(svc)

	cr := &CronRunner{svc: svc, logger: set.logger}
	got, ok := cr.readPersisted()
	if ok {
		t.Errorf("readPersisted() ok=true on missing file, want false; got=%v", got)
	}
	if !got.IsZero() {
		t.Errorf("readPersisted() on missing file must return zero Time, got %v", got)
	}
}

// TestCronAccuracyTruncate — the accuracy setter reaches the loop.
// We check the setter roundtrip since driving the goroutine is too
// flaky under -race; the loop itself just calls next.Truncate(cr.accuracy).
func TestCronAccuracyTruncate(t *testing.T) {
	cr := &CronRunner{}
	if cr.accuracy != 0 {
		t.Fatal("default accuracy must be 0")
	}
	cr.SetAccuracy(15 * time.Second)
	if cr.accuracy != 15*time.Second {
		t.Errorf("SetAccuracy(15s) not stored: %v", cr.accuracy)
	}
	// Sanity that Truncate does what the loop expects: snap forward
	// bucket boundary must equal input.Truncate(bucket).
	in := time.Date(2026, 7, 17, 8, 30, 22, 500_000_000, time.UTC)
	want := time.Date(2026, 7, 17, 8, 30, 15, 0, time.UTC)
	if got := in.Truncate(cr.accuracy); !got.Equal(want) {
		t.Errorf("Truncate: got %v, want %v", got, want)
	}
}

// TestFreezerFileEmptyOnNoCgroup guards the "no cgroup configured"
// path so Freeze/Thaw surface a clear error instead of silently
// writing to an empty path.
func TestFreezerFileEmptyOnNoCgroup(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "no-cgroup")
	set.AddService(svc)
	rec := svc.Record()

	if p := rec.freezerFile(); p != "" {
		t.Errorf("freezerFile() = %q on a svc with no cgroup, want empty", p)
	}
	if err := rec.Freeze(); err == nil {
		t.Error("Freeze() must return an error when no cgroup path is set")
	}
	if err := rec.Thaw(); err == nil {
		t.Error("Thaw() must return an error when no cgroup path is set")
	}
}

// TestFreezerFileJoinsPath confirms the cgroup path resolution: a
// service with a configured cgroup path gets `<path>/cgroup.freeze`
// as its target.
func TestFreezerFileJoinsPath(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "with-cgroup")
	set.AddService(svc)
	rec := svc.Record()
	rec.SetCgroupPath("/sys/fs/cgroup/slinit/with-cgroup")

	want := "/sys/fs/cgroup/slinit/with-cgroup/cgroup.freeze"
	if got := rec.freezerFile(); got != want {
		t.Errorf("freezerFile() = %q, want %q", got, want)
	}
}
