package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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

	if err := takeControl("c1", ":1.ctl", false); err != nil {
		t.Fatalf("first TakeControl: %v", err)
	}
	if err := takeControl("c1", ":1.ctl", false); err == nil {
		t.Error("second TakeControl without force should fail")
	}
	if err := takeControl("c1", ":1.ctl", true); err != nil {
		t.Errorf("TakeControl with force should displace the incumbent: %v", err)
	}
	// A different session is unaffected by c1's controller.
	if err := takeControl("c2", ":1.ctl", false); err != nil {
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

// TakeDevice used to resolve any char or block device and open it O_RDWR
// for the session's controller: an ordinary desktop user got /dev/mem,
// /dev/sda and every other node in /sys/dev. systemd-logind hands out
// character devices of the drm (card*), input (event*) and hidraw
// subsystems only (session_device_verify); anything else is ENODEV.
func TestTakeDeviceRefusesNonSessionDevices(t *testing.T) {
	resetDevices()
	defer resetDevices()

	// 1:3 is /dev/null and 1:1 /dev/mem, subsystem mem, on every Linux
	// system; 7:0 is /dev/vcs as a char device and loop0 as a block one.
	for _, d := range []devKey{{1, 3}, {1, 1}, {7, 0}} {
		if p, err := devPathForMajorMinor(d.major, d.minor); err == nil {
			t.Errorf("devPathForMajorMinor(%d, %d) = %s, want refused", d.major, d.minor, p)
		}
	}
	if _, _, err := takeDevice("c1", 1, 3); err == nil {
		t.Error("TakeDevice(1, 3) handed out /dev/null, want NoSuchDevice")
	}
}

// A session without a seat owns no devices: systemd refuses with EPERM
// because the device's seat is not the session's (session_device_verify).
// A remote or seatless session's owner could otherwise take the local
// console's keyboard and display.
func TestTakeDeviceRefusesSeatlessSession(t *testing.T) {
	m := usersBus(t)
	resetDevices()
	defer resetDevices()
	if err := writeJSONAtomic(sessionFile("c1"), SessionRecord{ID: "c1", UserID: 1000}); err != nil {
		t.Fatal(err)
	}
	so := &sessionObject{m: m, id: "c1"}
	if err := so.TakeControl(alice, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := so.TakeDevice(alice, 13, 64); !denied(err) {
		t.Errorf("TakeDevice on a seatless session: %v, want AccessDenied", err)
	}
}

// fakeSysDev adds char device major:minor to a sysfs tree of the test's
// own, laid out as the kernel does: /sys/dev/char/M:m links to the
// device's directory, whose subsystem link names its class.
func fakeSysDev(t *testing.T, major, minor uint32, subsystem, name, devname string) {
	t.Helper()
	if sysRoot == "/sys" {
		orig := sysRoot
		sysRoot = t.TempDir()
		t.Cleanup(func() { sysRoot = orig })
	}
	dir := filepath.Join(sysRoot, "devices", "virtual", subsystem, name)
	class := filepath.Join(sysRoot, "class", subsystem)
	for _, d := range []string{dir, class, filepath.Join(sysRoot, "dev", "char")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	uevent := fmt.Sprintf("MAJOR=%d\nMINOR=%d\nDEVNAME=%s\n", major, minor, devname)
	if err := os.WriteFile(filepath.Join(dir, "uevent"), []byte(uevent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(class, filepath.Join(dir, "subsystem")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, filepath.Join(sysRoot, "dev", "char", fmt.Sprintf("%d:%d", major, minor))); err != nil {
		t.Fatal(err)
	}
}

// What a compositor asks for still resolves: the card, input event
// nodes, hidraw. A drm render node, an input mouse node and any other
// class do not, as in systemd's detect_device_type.
func TestDevPathForSessionDevices(t *testing.T) {
	fakeSysDev(t, 226, 0, "drm", "card0", "dri/card0")
	fakeSysDev(t, 226, 128, "drm", "renderD128", "dri/renderD128")
	fakeSysDev(t, 13, 64, "input", "event0", "input/event0")
	fakeSysDev(t, 13, 32, "input", "mouse0", "input/mouse0")
	fakeSysDev(t, 245, 0, "hidraw", "hidraw0", "hidraw0")
	fakeSysDev(t, 1, 1, "mem", "mem", "mem")
	fakeSysDev(t, 4, 1, "tty", "tty1", "tty1")
	for _, c := range []struct {
		major, minor uint32
		want         string
	}{
		{226, 0, "/dev/dri/card0"},
		{13, 64, "/dev/input/event0"},
		{245, 0, "/dev/hidraw0"},
		{226, 128, ""},
		{13, 32, ""},
		{1, 1, ""},
		{4, 1, ""},
		{8, 0, ""}, // not a char device at all
	} {
		got, err := devPathForMajorMinor(c.major, c.minor)
		if c.want == "" && err == nil {
			t.Errorf("%d:%d resolved to %s, want refused", c.major, c.minor, got)
		}
		if c.want != "" && (err != nil || got != c.want) {
			t.Errorf("%d:%d = %q, %v; want %s", c.major, c.minor, got, err, c.want)
		}
	}
}

// ioW is the kernel's _IOW(type, nr, int).
func ioW(typ byte, nr uintptr) uintptr {
	const iocWrite, sizeofInt = 1, 4
	return iocWrite<<30 | sizeofInt<<16 | uintptr(typ)<<8 | nr
}

// A background session's hidraw node is revoked with HIDIOCREVOKE, as its
// input nodes are with EVIOCREVOKE (systemd's session_device_stop); it
// was left live. A card is not revoked (it loses master instead), nor is
// anything else.
func TestRevokeRequest(t *testing.T) {
	for _, c := range []struct {
		path string
		req  uintptr
	}{
		{"/dev/hidraw0", ioW('H', 0x0D)},
		{"/dev/input/event3", ioW('E', 0x91)},
		{"/dev/dri/card0", 0},
		{"/dev/null", 0},
	} {
		req, ok := revokeRequest(c.path)
		if ok != (c.req != 0) || req != c.req {
			t.Errorf("revokeRequest(%s) = %#x, %v; want %#x", c.path, req, ok, c.req)
		}
	}
}

// A revoked hidraw descriptor cannot be revived, so a resume reopens the
// node; one that cannot be reopened stays paused. It used to be resumed
// with the old descriptor.
func TestHidrawResumeReopens(t *testing.T) {
	resetDevices()
	t.Cleanup(resetDevices)
	notes := captureNotes(t)
	null := openNull(t)
	fd, err := syscall.Dup(int(null.Fd()))
	null.Close()
	if err != nil {
		t.Fatal(err)
	}
	dev := os.NewFile(uintptr(fd), "/dev/hidraw-slinit-test-absent")
	key := devKey{245, 0}
	devicesMu.Lock()
	devices["c1"] = &sessionDevices{controlled: true, controller: ":1.10", vtnr: 3,
		files: map[devKey]*os.File{key: dev}, paused: map[devKey]bool{key: true}}
	devicesMu.Unlock()

	setVT(t, 3)
	(&manager{}).syncDevicesToVT()
	if len(*notes) != 0 {
		t.Errorf("resumed a hidraw node that could not be reopened: %+v", *notes)
	}
	devicesMu.Lock()
	defer devicesMu.Unlock()
	if !devices["c1"].paused[key] {
		t.Error("the hidraw node is no longer paused")
	}
}
