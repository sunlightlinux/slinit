package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// watcherWithStub builds a watcher whose actions reach a fake control
// CLI instead of the kernel, and returns the file its invocations are
// recorded in.
func watcherWithStub(t *testing.T, cfg buttonConfig) (*buttonWatcher, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "slinitctl")
	log := filepath.Join(dir, "calls")
	body := "#!/bin/sh\necho \"$@\" >> " + log + "\n"
	if err := os.WriteFile(bin, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	prev := slinitctlSuspendPath
	slinitctlSuspendPath = bin
	t.Cleanup(func() { slinitctlSuspendPath = prev })

	m := &manager{inhibitors: newInhibitRegistry(), buttons: cfg}
	return newButtonWatcher(m, cfg), log
}

func calls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return ""
	}
	return string(b)
}

// The headline behaviour: a lid-close event with HandleLidSwitch=suspend
// suspends. Before the watcher existed, closing the lid did nothing at
// all.
func TestLidCloseSuspends(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.LidSwitch = actionSuspend
	cfg.LidSwitchExternalPower = actionSuspend
	cfg.LidSwitchDocked = actionSuspend
	cfg.HoldoffTimeout = 0
	w, log := watcherWithStub(t, cfg)

	w.handle(evSW, swLID, 1)

	if got := calls(t, log); !strings.Contains(got, "suspend mem") {
		t.Errorf("lid close did not suspend; invocations: %q", got)
	}
}

// Lid *open* must do nothing — acting on both edges would suspend on the
// way out of a suspend.
func TestLidOpenDoesNothing(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.LidSwitch = actionSuspend
	cfg.HoldoffTimeout = 0
	w, log := watcherWithStub(t, cfg)

	w.handle(evSW, swLID, 0)

	if got := calls(t, log); got != "" {
		t.Errorf("lid open should not act, got %q", got)
	}
}

// The holdoff exists because a lid switch that still reads "closed" as
// the machine resumes would suspend it again at once — which an operator
// experiences as a laptop that will not wake up.
func TestHoldoffSuppressesLidClose(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.LidSwitch = actionSuspend
	cfg.LidSwitchExternalPower = actionSuspend
	cfg.LidSwitchDocked = actionSuspend
	cfg.HoldoffTimeout = time.Hour
	w, log := watcherWithStub(t, cfg)
	w.armHoldoff()

	w.handle(evSW, swLID, 1)
	if got := calls(t, log); got != "" {
		t.Errorf("lid close inside the holdoff should be ignored, got %q", got)
	}

	// Opening the lid means the machine is in use: the window clears.
	w.handle(evSW, swLID, 0)
	w.handle(evSW, swLID, 1)
	if got := calls(t, log); !strings.Contains(got, "suspend mem") {
		t.Errorf("after a lid open the next close should act; got %q", got)
	}
}

// A successful sleep re-arms the holdoff, because the call returning
// means the machine woke up.
func TestSleepReArmsHoldoff(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.LidSwitch = actionSuspend
	cfg.LidSwitchExternalPower = actionSuspend
	cfg.LidSwitchDocked = actionSuspend
	cfg.HoldoffTimeout = time.Hour
	w, _ := watcherWithStub(t, cfg)

	if w.inHoldoff() {
		t.Fatal("setup: should not start in a holdoff window here")
	}
	w.handle(evSW, swLID, 1)
	if !w.inHoldoff() {
		t.Error("the holdoff should be re-armed after waking from a suspend")
	}
}

// Keys route to their own settings, on press only.
func TestKeyCodesRouteToTheirActions(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.PowerKey = actionSuspend // not poweroff: the stub would not show which
	cfg.SuspendKey = actionHibernate
	cfg.HoldoffTimeout = 0
	w, log := watcherWithStub(t, cfg)

	w.handle(evKey, keyPower, 1)
	if got := calls(t, log); !strings.Contains(got, "suspend mem") {
		t.Errorf("power key did not act: %q", got)
	}

	w.handle(evKey, keySleep, 1)
	if got := calls(t, log); !strings.Contains(got, "suspend disk") {
		t.Errorf("sleep key did not hibernate: %q", got)
	}

	// Release must not act again.
	before := calls(t, log)
	w.handle(evKey, keyPower, 0)
	if calls(t, log) != before {
		t.Error("key release ran the action a second time")
	}
}

// An unconfigured key is ignored even though the device reports it.
func TestUnconfiguredKeyIgnored(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.HoldoffTimeout = 0
	w, log := watcherWithStub(t, cfg)

	w.handle(evKey, keyPower, 1)
	w.handle(evSW, swLID, 1)
	if got := calls(t, log); got != "" {
		t.Errorf("nothing is configured, nothing should run; got %q", got)
	}
}

// A block lock on the handler class stops the action — that is what
// "systemd-inhibit --what=handle-lid-switch" is for, and a presentation
// that keeps running when the lid is shut depends on it.
func TestInhibitorBlocksLidAction(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.LidSwitch = actionSuspend
	cfg.LidSwitchExternalPower = actionSuspend
	cfg.LidSwitchDocked = actionSuspend
	cfg.HoldoffTimeout = 0
	w, log := watcherWithStub(t, cfg)

	client := addDirect(t, w.m.inhibitors, "handle-lid-switch", "block")
	w.handle(evSW, swLID, 1)
	if got := calls(t, log); got != "" {
		t.Errorf("an inhibited lid switch should not act, got %q", got)
	}

	// Once released, it acts again.
	client.Close()
	waitUntil(t, 2*time.Second, "the lock to clear", func() bool {
		return w.m.inhibitors.blockedBy("handle-lid-switch") == nil
	})
	w.handle(evSW, swLID, 1)
	if got := calls(t, log); !strings.Contains(got, "suspend mem") {
		t.Errorf("after release the lid should act; got %q", got)
	}
}

// A lock on a different class must not block this one.
func TestUnrelatedInhibitorDoesNotBlock(t *testing.T) {
	cfg := defaultButtonConfig()
	cfg.LidSwitch = actionSuspend
	cfg.LidSwitchExternalPower = actionSuspend
	cfg.LidSwitchDocked = actionSuspend
	cfg.HoldoffTimeout = 0
	w, log := watcherWithStub(t, cfg)

	client := addDirect(t, w.m.inhibitors, "handle-power-key", "block")
	defer client.Close()

	w.handle(evSW, swLID, 1)
	if got := calls(t, log); !strings.Contains(got, "suspend mem") {
		t.Errorf("a power-key lock must not block the lid; got %q", got)
	}
}

// The ioctl request numbers are computed rather than copied from a
// header, so they are worth pinning: a wrong one silently returns EINVAL
// and every device looks uninteresting, which would present as "the lid
// does nothing" with no error anywhere.
func TestIoctlRequestEncoding(t *testing.T) {
	// The literal the kernel's _IOC produces for EVIOCGBIT(EV_SW, 96):
	// dir=2 at bit 30, size=96 at bit 16, type 'E' (0x45) at bit 8,
	// nr=0x20+EV_SW. Pinned as a number as well as a formula, because an
	// error in the formula would otherwise be compared against itself.
	if got := iocR('E', 0x20+evSW, 96); got != 0x80604525 {
		t.Errorf("EVIOCGBIT(EV_SW,96) = %#x, want 0x80604525", got)
	}
	// And spelled out, which is what catches a shift being changed.
	want := uintptr(2)<<30 | uintptr(96)<<16 | uintptr('E')<<8 | uintptr(0x20+evSW)
	if got := iocR('E', 0x20+evSW, 96); got != want {
		t.Errorf("EVIOCGBIT = %#x, want %#x", got, want)
	}
	wantSW := uintptr(2)<<30 | uintptr(96)<<16 | uintptr('E')<<8 | 0x1b
	if got := iocR('E', 0x1b, 96); got != wantSW {
		t.Errorf("EVIOCGSW = %#x, want %#x", got, wantSW)
	}

	// Taken from the kernel's own macros rather than re-derived, by
	// compiling linux/input.h on 2026-10-01:
	//
	//	EVIOCGBIT(EV_SW, 96)  = 0x80604525
	//	EVIOCGBIT(EV_KEY, 96) = 0x80604521
	//	EVIOCGSW(96)          = 0x8060451b
	//
	// A wrong request number does not fail loudly — the ioctl returns
	// EINVAL, every device then looks uninteresting, and the symptom is
	// a lid that does nothing with no error anywhere. Worth pinning
	// against the source of truth.
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"EVIOCGBIT(EV_SW,96)", iocR('E', 0x20+evSW, 96), 0x80604525},
		{"EVIOCGBIT(EV_KEY,96)", iocR('E', 0x20+evKey, 96), 0x80604521},
		{"EVIOCGSW(96)", iocR('E', 0x1b, 96), 0x8060451b},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %#x, kernel header says %#x", tc.name, tc.got, tc.want)
		}
	}

	// And the codes themselves, same source.
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"EV_SW", evSW, 5}, {"EV_KEY", evKey, 1},
		{"SW_LID", swLID, 0}, {"SW_DOCK", swDOCK, 5},
		{"KEY_POWER", keyPower, 116}, {"KEY_SLEEP", keySleep, 142},
		{"KEY_SUSPEND", keySuspend, 205},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, kernel header says %d", tc.name, tc.got, tc.want)
		}
	}

	// struct input_event is 24 bytes on this architecture; a mis-sized
	// struct would decode every field from the wrong offset and read
	// garbage codes.
	if evdevEventSize != 24 {
		t.Errorf("sizeof(input_event) = %d, kernel says 24 on amd64", evdevEventSize)
	}
}

func TestBitSet(t *testing.T) {
	b := make([]byte, 4)
	b[0] = 1 << 3
	b[2] = 1 << 1
	if !bitSet(b, 3) {
		t.Error("bit 3 should be set")
	}
	if !bitSet(b, 17) {
		t.Error("bit 17 should be set")
	}
	if bitSet(b, 4) {
		t.Error("bit 4 should not be set")
	}
	if bitSet(b, 999) {
		t.Error("a bit past the bitmap must read as unset, not panic")
	}
}
