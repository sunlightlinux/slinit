package main

import (
	"os"
	"testing"
)

// resetDevices clears the package-global device table between cases.
func resetDevices() {
	devicesMu.Lock()
	defer devicesMu.Unlock()
	for id := range devices {
		closeSessionDevicesLocked(id)
		delete(devices, id)
	}
}

func TestTakeControlIsExclusive(t *testing.T) {
	resetDevices()
	defer resetDevices()

	if err := takeControl("c1", false); err != nil {
		t.Fatalf("first TakeControl: %v", err)
	}
	if err := takeControl("c1", false); err == nil {
		t.Error("second TakeControl without force should fail")
	}
	if err := takeControl("c1", true); err != nil {
		t.Errorf("TakeControl with force should displace the incumbent: %v", err)
	}
	// A different session is unaffected by c1's controller.
	if err := takeControl("c2", false); err != nil {
		t.Errorf("TakeControl on a second session: %v", err)
	}
}

// TestReleaseControlClosesDevices is the regression guard for the fd
// leak: TakeDevice used to hand out a descriptor and keep ours open
// forever, so a compositor restart left a DRM fd nobody could close.
func TestReleaseControlClosesDevices(t *testing.T) {
	resetDevices()
	defer resetDevices()

	// /dev/null stands in for a device node: it is always present and,
	// not being under /dev/dri, skips the DRM master ioctls.
	f, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}

	devicesMu.Lock()
	devices["c1"] = &sessionDevices{
		controlled: true,
		files:      map[devKey]*os.File{{major: 1, minor: 3}: f},
	}
	devicesMu.Unlock()

	releaseControl("c1")

	if _, err := f.Stat(); err == nil {
		t.Error("ReleaseControl left the descriptor open")
	}
	devicesMu.Lock()
	_, stillThere := devices["c1"]
	devicesMu.Unlock()
	if stillThere {
		t.Error("ReleaseControl left the session in the device table")
	}
}

func TestReleaseDeviceClosesOnlyThatDevice(t *testing.T) {
	resetDevices()
	defer resetDevices()

	a, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}
	b, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}

	devicesMu.Lock()
	devices["c1"] = &sessionDevices{
		controlled: true,
		files: map[devKey]*os.File{
			{major: 1, minor: 3}: a,
			{major: 1, minor: 5}: b,
		},
	}
	devicesMu.Unlock()

	releaseDevice("c1", 1, 3)

	if _, err := a.Stat(); err == nil {
		t.Error("released device is still open")
	}
	if _, err := b.Stat(); err != nil {
		t.Error("ReleaseDevice closed a device it was not asked to")
	}

	devicesMu.Lock()
	n := len(devices["c1"].files)
	devicesMu.Unlock()
	if n != 1 {
		t.Errorf("device table has %d entries, want 1", n)
	}

	// Releasing something we never held must not panic or disturb the
	// rest of the table.
	releaseDevice("c1", 9, 9)
	releaseDevice("no-such-session", 1, 3)
}

func TestIsDRMNode(t *testing.T) {
	for path, want := range map[string]bool{
		"/dev/dri/card0":      true,
		"/dev/dri/renderD128": true,
		"/dev/input/event3":   false,
		"/dev/null":           false,
	} {
		if got := isDRMNode(path); got != want {
			t.Errorf("isDRMNode(%q) = %v, want %v", path, got, want)
		}
	}
}
