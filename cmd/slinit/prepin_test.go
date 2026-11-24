package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/config"
	"github.com/sunlightlinux/slinit/pkg/persist"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/snapshot"
)

// bootSetWithFoo returns a set whose boot service waits-for foo, so
// starting boot starts foo unless foo is pinned stopped.
func bootSetWithFoo(t *testing.T) *service.ServiceSet {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"boot": "type = internal\nwaits-for = foo\n",
		"foo":  "type = internal\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	set := service.NewServiceSet(quietSvcLogger{})
	set.SetLoader(config.NewDirLoader(set, []string{dir}))
	return set
}

// startBoot is the boot loop's part that matters here.
func startBoot(t *testing.T, set *service.ServiceSet) {
	t.Helper()
	boot, err := set.LoadService("boot")
	if err != nil {
		t.Fatal(err)
	}
	set.StartService(boot)
}

func checkFooPinnedStopped(t *testing.T, set *service.ServiceSet) {
	t.Helper()
	foo := set.FindService("foo", false)
	if foo == nil {
		t.Fatal("foo not loaded")
	}
	if foo.State() != service.StateStopped || !foo.Record().IsStopPinned() {
		t.Errorf("foo is %v, pinnedStop=%v; want STOPPED and pinned",
			foo.State(), foo.Record().IsStopPinned())
	}
}

// A service pinned stopped before a soft reboot, in the boot graph:
// the pin must be in place before the boot cascade, or the cascade
// starts it and the later restore only sets the pin on a running
// service.
func TestSnapshotStopPinAppliedBeforeBoot(t *testing.T) {
	set := bootSetWithFoo(t)
	path := filepath.Join(t.TempDir(), "snap.json")
	snap := &snapshot.Snapshot{Version: snapshot.CurrentVersion,
		Services: []snapshot.ServiceSnapshot{{Name: "foo", PinnedStop: true}}}
	if err := snapshot.Write(path, snap); err != nil {
		t.Fatal(err)
	}

	pinStopsBeforeBoot(set, path, persist.NewPinStore(""))
	startBoot(t, set)
	if _, err := snapshot.Restore(set, snap, nil); err != nil {
		t.Fatal(err)
	}
	checkFooPinnedStopped(t, set)
}

// The same for a pin persisted with --persist-intent.
func TestPersistedStopPinAppliedBeforeBoot(t *testing.T) {
	set := bootSetWithFoo(t)
	pins := persist.NewPinStore(t.TempDir())
	if err := pins.Set("foo", persist.IntentPinnedStopped); err != nil {
		t.Fatal(err)
	}

	pinStopsBeforeBoot(set, "", pins)
	startBoot(t, set)
	checkFooPinnedStopped(t, set)
}
