package service

import (
	"os"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// fdstoreScratch returns a real *os.File the store can hold. Reads
// from it after storage prove the fd is still open (a Close() would
// have made the returned handle unusable, but our second copy in the
// store is independent — dup via File so a Read on the store's copy
// works even after we let the scratch var go out of scope).
func fdstoreScratch(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fdstore-scratch")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	if _, err := f.WriteString("payload"); err != nil {
		t.Fatalf("write: %v", err)
	}
	return f
}

// TestFDStorePreservedAcrossRestart is the regression test for the ceres
// v1.10.41 bug: acceptance test 63-fdstore observed LISTEN_FDS=""  on
// the child spawned by `slinitctl restart`, because Stopped() closed
// the fdstore unconditionally when preserve="" (the systemd default).
//
// Post-fix contract: when the service's desired state remains STARTED
// through Stopped() (Restart() sets this — willRestart=true), the store
// MUST survive regardless of file-descriptor-store-preserve. The
// directive only kicks in for final deactivation.
func TestFDStorePreservedAcrossRestart(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "fds-restart")
	set.AddService(svc)
	rec := svc.Record()

	rec.SetFDStoreMax(2)
	if rec.FDStore() == nil {
		t.Fatal("SetFDStoreMax(2) did not allocate an fdStore")
	}
	// preserve unset — this is precisely the failing configuration.
	rec.SetFDStorePreserve("")

	f := fdstoreScratch(t)
	if err := rec.FDStore().Add(process.FDStoreEntry{Name: "s", File: f}); err != nil {
		t.Fatalf("store add: %v", err)
	}
	if rec.FDStore().Len() != 1 {
		t.Fatalf("pre-Stopped store len=%d, want 1", rec.FDStore().Len())
	}

	// Simulate the state doStop leaves behind on a user-driven Restart:
	// desired stays STARTED, state is STOPPING as we enter Stopped().
	rec.desired.Store(StateStarted)
	rec.state.Store(StateStopping)
	rec.Stopped()
	set.ProcessQueues()

	if rec.FDStore().Len() != 1 {
		t.Errorf("fdstore lost entry across restart: len=%d, want 1", rec.FDStore().Len())
	}
}

// TestFDStoreClosedOnFinalDeactivation confirms the default-preserve
// path still closes the store on true deactivation, i.e. when
// willRestart=false. Guards against the fix over-preserving.
func TestFDStoreClosedOnFinalDeactivation(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "fds-final")
	set.AddService(svc)
	rec := svc.Record()

	rec.SetFDStoreMax(2)
	rec.SetFDStorePreserve("")

	f := fdstoreScratch(t)
	if err := rec.FDStore().Add(process.FDStoreEntry{Name: "s", File: f}); err != nil {
		t.Fatalf("store add: %v", err)
	}

	// Final deactivation: desired=Stopped (user asked stop, no restart).
	rec.desired.Store(StateStopped)
	rec.state.Store(StateStopping)
	rec.Stopped()
	set.ProcessQueues()

	if rec.FDStore().Len() != 0 {
		t.Errorf("fdstore kept entries on final deactivation with preserve=\"\": len=%d, want 0", rec.FDStore().Len())
	}
}

// TestFDStorePreservedAcrossFinalDeactivationWhenPreserveYes covers the
// preserve=yes branch: even a full deactivation must not close the
// store.
func TestFDStorePreservedAcrossFinalDeactivationWhenPreserveYes(t *testing.T) {
	set, _ := newTestSet()
	svc := NewInternalService(set, "fds-yes")
	set.AddService(svc)
	rec := svc.Record()

	rec.SetFDStoreMax(2)
	rec.SetFDStorePreserve("yes")

	f := fdstoreScratch(t)
	if err := rec.FDStore().Add(process.FDStoreEntry{Name: "s", File: f}); err != nil {
		t.Fatalf("store add: %v", err)
	}

	rec.desired.Store(StateStopped)
	rec.state.Store(StateStopping)
	rec.Stopped()
	set.ProcessQueues()

	if rec.FDStore().Len() != 1 {
		t.Errorf("fdstore lost entry with preserve=yes on final deactivation: len=%d, want 1", rec.FDStore().Len())
	}
}
