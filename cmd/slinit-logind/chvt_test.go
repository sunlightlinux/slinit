package main

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/pkg/polkit"
)

// Switching VTs was open to anyone: an ssh user could flip the console,
// or bring their own inactive local session to the front and with it the
// active session's power rights. It is now polkit's
// org.freedesktop.login1.chvt with its defaults: root, the user of the
// session in the foreground of the seat, and whom polkitd allows.
func TestVTSwitchNeedsActiveLocalSession(t *testing.T) {
	w := newPowerWorld(t, 2)
	w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2, TTY: "tty2"})
	w.session(SessionRecord{ID: "c5", UserID: 1002, SeatID: "seat0", VTNr: 5, TTY: "tty5"})
	w.session(SessionRecord{ID: "c7", UserID: 1001, Remote: true})
	// A session with no VT: activating it switches nothing, so an allowed
	// call can be made here without touching the console.
	w.session(SessionRecord{ID: "c9", UserID: 1000, SeatID: "seat0"})
	desk := w.caller(":1.desk", 1000, 4242, "c2")
	ssh := w.caller(":1.ssh", 1001, 777, "c7")
	back := w.caller(":1.back", 1002, 555, "c5")
	root := w.caller(":1.root", 0, 1, "")

	for _, c := range []struct {
		name   string
		sender dbus.Sender
		seat   string
		allow  bool
	}{
		{"root", root, "seat0", true},
		{"user of the session in front", desk, "seat0", true},
		{"user in front, another seat", desk, "seat1", false},
		{"ssh user", ssh, "seat0", false},
		{"user of a background session", back, "seat0", false},
		{"unknown caller", ":1.ghost", "seat0", false},
	} {
		err := w.m.mayChvt(c.sender, c.seat, "test")
		if c.allow && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.allow && !denied(err) {
			t.Errorf("%s: %v, want AccessDenied", c.name, err)
		}
	}

	so := &sessionObject{m: w.m, id: "c5"}
	seat := &seatObject{m: w.m, id: "seat0"}
	for _, caller := range []dbus.Sender{ssh, back} {
		for name, err := range map[string]*dbus.Error{
			"ActivateSession":       w.m.ActivateSession(caller, "c5"),
			"ActivateSessionOnSeat": w.m.ActivateSessionOnSeat(caller, "c5", "seat0"),
			"Session.Activate":      so.Activate(caller),
			"Seat.ActivateSession":  seat.ActivateSession(caller, "c5"),
			"Seat.SwitchTo":         seat.SwitchTo(caller, 5),
		} {
			if !denied(err) {
				t.Errorf("%s from %s: %v, want AccessDenied", name, caller, err)
			}
		}
	}
	if err := w.m.ActivateSession(desk, "c9"); err != nil {
		t.Errorf("ActivateSession from the user in front: %v", err)
	}
}

// Whoever the rules above refuse is polkit's to decide, and without an
// answer from polkitd the switch is refused.
func TestVTSwitchAsksPolkit(t *testing.T) {
	w := newPowerWorld(t, 2)
	w.session(SessionRecord{ID: "c7", UserID: 1001, Remote: true})
	ssh := w.caller(":1.ssh", 1001, 777, "c7")
	var asked string
	answer := func(ok bool, err error) {
		w.m.polkit = &polkit.Checker{
			UID: func(s dbus.Sender) (uint32, error) { return w.uid[s], nil },
			Check: func(_ dbus.Sender, action string, _ map[string]string, interactive bool) (bool, bool, error) {
				asked = action
				return ok, false, err
			},
		}
	}
	answer(true, nil)
	if err := w.m.mayChvt(ssh, "seat0", "test"); err != nil || asked != chvtAction {
		t.Errorf("polkit allows: %v (asked %q)", err, asked)
	}
	answer(false, nil)
	if err := w.m.mayChvt(ssh, "seat0", "test"); !denied(err) {
		t.Errorf("polkit refuses: %v, want AccessDenied", err)
	}
	answer(false, errors.New("org.freedesktop.DBus.Error.ServiceUnknown"))
	if err := w.m.mayChvt(ssh, "seat0", "test"); !denied(err) {
		t.Errorf("no polkitd: %v, want AccessDenied", err)
	}
}
