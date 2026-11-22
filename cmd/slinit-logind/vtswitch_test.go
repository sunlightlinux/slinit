package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// captureNotes replaces the signal sender with one that records what
// would have gone out, checking each resume descriptor is still open
// when it is sent.
func captureNotes(t *testing.T) *[]devNote {
	t.Helper()
	var got []devNote
	prev := sendDevNoteFunc
	sendDevNoteFunc = func(_ *manager, n devNote) error {
		if n.fd != nil {
			if _, err := n.fd.Stat(); err != nil {
				t.Errorf("%s %v: descriptor already closed when sent", n.member, n.key)
			}
		}
		got = append(got, n)
		return nil
	}
	t.Cleanup(func() { sendDevNoteFunc = prev })
	return &got
}

func setVT(t *testing.T, vt uint32) {
	t.Helper()
	prev := currentVT.Load()
	currentVT.Store(vt)
	t.Cleanup(func() { currentVT.Store(prev) })
}

func openNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}
	return f
}

// The Switch User hang on sl-demo1: nothing paused the session being
// left or resumed the one coming back. On a switch the session leaving
// the front is paused and the one arriving resumed, and each controller
// hears only about its own devices.
func TestVTSwitchPausesAndResumes(t *testing.T) {
	resetDevices()
	t.Cleanup(resetDevices)
	notes := captureNotes(t)
	m := &manager{}

	devicesMu.Lock()
	devices["c1"] = &sessionDevices{controlled: true, controller: ":1.10", vtnr: 3,
		files: map[devKey]*os.File{{226, 0}: openNull(t)}}
	devices["c2"] = &sessionDevices{controlled: true, controller: ":1.20", vtnr: 4,
		files: map[devKey]*os.File{{13, 64}: openNull(t)}}
	devicesMu.Unlock()

	setVT(t, 3)
	m.syncDevicesToVT()
	if len(*notes) != 1 || (*notes)[0].member != "PauseDevice" || (*notes)[0].controller != ":1.20" {
		t.Fatalf("with tty3 in front only c2 should be paused, got %+v", *notes)
	}

	*notes = nil
	m.syncDevicesToVT()
	if len(*notes) != 0 {
		t.Fatalf("an unchanged VT must not pause or resume again, got %+v", *notes)
	}

	currentVT.Store(4)
	m.syncDevicesToVT()
	var paused, resumed []devNote
	for _, n := range *notes {
		switch n.member {
		case "PauseDevice":
			paused = append(paused, n)
		case "ResumeDevice":
			resumed = append(resumed, n)
		}
	}
	if len(paused) != 1 || paused[0].controller != ":1.10" || paused[0].key != (devKey{226, 0}) {
		t.Errorf("switching to tty4 should pause c1's card for :1.10, got %+v", paused)
	}
	if len(resumed) != 1 || resumed[0].controller != ":1.20" || resumed[0].key != (devKey{13, 64}) || resumed[0].fd == nil {
		t.Errorf("switching to tty4 should resume c2's device for :1.20 with a descriptor, got %+v", resumed)
	}
	if resumed != nil && resumed[0].fd != nil {
		if _, err := resumed[0].fd.Stat(); err == nil {
			t.Error("the duplicate sent with ResumeDevice was not closed after sending")
		}
	}
	devicesMu.Lock()
	defer devicesMu.Unlock()
	if !devices["c1"].paused[devKey{226, 0}] || devices["c2"].paused[devKey{13, 64}] {
		t.Errorf("paused state after the switch: c1 %v, c2 %v", devices["c1"].paused, devices["c2"].paused)
	}
}

// A device asked for by a session in the background comes back paused
// and flagged inactive; claiming DRM master for it would take the
// screen from the session in front.
func TestTakeDeviceInBackgroundIsInactive(t *testing.T) {
	if _, err := devPathForMajorMinor(1, 3); err != nil {
		t.Skipf("no /sys/dev entry for /dev/null: %v", err)
	}
	resetDevices()
	t.Cleanup(resetDevices)
	setVT(t, 3)

	devicesMu.Lock()
	devices["c2"] = &sessionDevices{controlled: true, controller: ":1.20", vtnr: 4, files: map[devKey]*os.File{}}
	devices["c1"] = &sessionDevices{controlled: true, controller: ":1.10", vtnr: 3, files: map[devKey]*os.File{}}
	devicesMu.Unlock()

	if _, inactive, err := takeDevice("c2", 1, 3); err != nil || !inactive {
		t.Errorf("background session: inactive=%v err=%v, want inactive", inactive, err)
	}
	if _, inactive, err := takeDevice("c2", 1, 3); err != nil || !inactive {
		t.Errorf("re-take in the background: inactive=%v err=%v, want inactive", inactive, err)
	}
	if _, inactive, err := takeDevice("c1", 1, 3); err != nil || inactive {
		t.Errorf("front session: inactive=%v err=%v, want active", inactive, err)
	}
	devicesMu.Lock()
	defer devicesMu.Unlock()
	if !devices["c2"].paused[devKey{1, 3}] || devices["c1"].paused[devKey{1, 3}] {
		t.Errorf("paused: c2 %v, c1 %v", devices["c2"].paused, devices["c1"].paused)
	}
}

// ResumeDevice carries a descriptor for the device — for an input node,
// the keyboard. It must be addressed to the controller alone, never
// broadcast.
func TestDevNoteIsUnicast(t *testing.T) {
	f := openNull(t)
	defer f.Close()
	for _, c := range []struct {
		n   devNote
		sig string
	}{
		{devNote{session: "c5", controller: ":1.18", member: "ResumeDevice", key: devKey{13, 65}, fd: f}, "uuh"},
		{devNote{session: "c5", controller: ":1.18", member: "PauseDevice", key: devKey{226, 0}}, "uus"},
	} {
		msg := devNoteMessage(c.n)
		if msg.Type != dbus.TypeSignal {
			t.Errorf("%s: type %v, want a signal", c.n.member, msg.Type)
		}
		if d, _ := msg.Headers[dbus.FieldDestination].Value().(string); d != ":1.18" {
			t.Errorf("%s: destination %q, want the controller :1.18", c.n.member, d)
		}
		if s, _ := msg.Headers[dbus.FieldSignature].Value().(dbus.Signature); s.String() != c.sig {
			t.Errorf("%s: signature %q, want %q", c.n.member, s.String(), c.sig)
		}
		if p, _ := msg.Headers[dbus.FieldPath].Value().(dbus.ObjectPath); p != sessionPath("c5") {
			t.Errorf("%s: path %q", c.n.member, p)
		}
		if err := msg.IsValid(); err != nil {
			t.Errorf("%s: invalid message: %v", c.n.member, err)
		}
	}
	if m := devNoteMessage(devNote{session: "c5", controller: ":1.18", member: "PauseDevice", key: devKey{226, 0}}); m.Body[2] != "force" {
		t.Errorf("PauseDevice reason %v, want force", m.Body[2])
	}
}

// Releasing control gives the VT back: once nobody controls the session
// nothing would answer the kernel's release request.
func TestReleaseControlRestoresVT(t *testing.T) {
	resetDevices()
	t.Cleanup(resetDevices)
	f := openNull(t) // stands in for the VT; the ioctls fail harmlessly
	devicesMu.Lock()
	devices["c1"] = &sessionDevices{controlled: true, controller: ":1.10", vtnr: 3, vt: f, files: map[devKey]*os.File{}}
	devicesMu.Unlock()

	releaseControl("c1")
	if _, err := f.Stat(); err == nil {
		t.Error("ReleaseControl kept the VT open")
	}
}

// attachVT must not hold a VT for a caller that is not, or is no longer,
// the controller — a forced TakeControl may have displaced it.
func TestAttachVTOnlyForTheController(t *testing.T) {
	resetDevices()
	t.Cleanup(resetDevices)
	m := &manager{}
	if err := takeControl("c1", ":1.10", false); err != nil {
		t.Fatal(err)
	}
	m.attachVT("c1", ":1.10", 4, 1000)
	m.attachVT("c1", ":1.99", 3, 1000)
	devicesMu.Lock()
	defer devicesMu.Unlock()
	if devices["c1"].vtnr != 4 {
		t.Errorf("vtnr %d, want the controller's 4", devices["c1"].vtnr)
	}
	if devices["c1"].vt != nil {
		t.Error("VT taken although VT handling is off in tests")
	}
}

// The kernel sends the VT's release signal to this process, and a
// signal Go does not deliver kills it: 34, the first choice, is reserved
// by the Go runtime, so Notify silently did nothing and every Switch
// User killed slinit-logind. Sent here for real; with an undeliverable
// signal this test dies the same way.
func TestVTSignalsAreDelivered(t *testing.T) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, vtRelSig, vtAcqSig)
	defer signal.Stop(ch)
	for _, sig := range []syscall.Signal{vtRelSig, vtAcqSig} {
		if err := syscall.Kill(os.Getpid(), sig); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-ch:
			if got != sig {
				t.Errorf("sent %v, received %v", sig, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("signal %d was not delivered", int(sig))
		}
	}
}
