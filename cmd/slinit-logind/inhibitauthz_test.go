package main

import (
	"errors"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
)

// Polkit's defaults for org.freedesktop.login1.inhibit-*: delay locks on
// shutdown and sleep, and idle blocks, are anyone's; block locks on
// shutdown and sleep and the handle-* key locks need a local session,
// foreground or not. Inhibit used to be root's alone, and gnome-session
// logged "Failed to get delay inhibitor: access denied" on every phase.
func TestInhibitFollowsPolkitDefaults(t *testing.T) {
	w := newPowerWorld(t, 2)
	w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2, TTY: "tty2"})
	w.session(SessionRecord{ID: "c3", UserID: 1001, SeatID: "seat0", VTNr: 3, TTY: "tty3"})
	w.session(SessionRecord{ID: "r1", UserID: 1002, Remote: true})
	desk := w.caller(":1.desk", 1000, 4242, "c2")   // foreground
	bg := w.caller(":1.bg", 1001, 4343, "c3")       // local, background
	remote := w.caller(":1.ssh", 1002, 4444, "r1")  // remote
	nobody := w.caller(":1.daemon", 1003, 4545, "") // in no session
	rootc := w.caller(":1.root", 0, 1, "")

	cases := []struct {
		what, mode string
		allowed    []dbus.Sender
		refused    []dbus.Sender
	}{
		{"sleep", "delay", []dbus.Sender{desk, bg, remote, nobody, rootc}, nil},
		{"shutdown", "delay", []dbus.Sender{desk, bg, remote, nobody, rootc}, nil},
		{"idle", "block", []dbus.Sender{desk, bg, remote, nobody, rootc}, nil},
		{"sleep", "block", []dbus.Sender{desk, bg, rootc}, []dbus.Sender{remote, nobody}},
		{"shutdown", "block", []dbus.Sender{desk, bg, rootc}, []dbus.Sender{remote, nobody}},
		{"handle-power-key:handle-lid-switch", "block", []dbus.Sender{desk, bg, rootc}, []dbus.Sender{remote, nobody}},
		{"handle-suspend-key", "delay", []dbus.Sender{desk, rootc}, []dbus.Sender{remote, nobody}},
		// One class needing a session makes the whole lock need one.
		{"sleep:handle-power-key", "delay", []dbus.Sender{desk, rootc}, []dbus.Sender{remote, nobody}},
	}
	for _, c := range cases {
		for _, s := range c.allowed {
			if err := w.m.mayInhibit(s, c.what, c.mode); err != nil {
				t.Errorf("%s %s from %s: refused: %v", c.what, c.mode, s, err)
			}
		}
		for _, s := range c.refused {
			if !denied(w.m.mayInhibit(s, c.what, c.mode)) {
				t.Errorf("%s %s from %s: allowed, want AccessDenied", c.what, c.mode, s)
			}
		}
	}
	if !denied(w.m.mayInhibit(":1.gone", "sleep", "delay")) {
		t.Error("an unidentifiable caller must be refused")
	}

	// Through the D-Bus entry point, so a method that skips the rule fails.
	fd, err := w.m.Inhibit(remote, "sleep", "screen locker", "lock first", "delay")
	if err != nil {
		t.Fatalf("Inhibit delay from a user: %v", err)
	}
	syscall.Close(int(fd))
	if _, err := w.m.Inhibit(remote, "shutdown", "x", "y", "block"); !denied(err) {
		t.Errorf("Inhibit block from a remote user: got %v, want AccessDenied", err)
	}
}

// Anyone may take a delay lock, so the number held is capped, as
// systemd's InhibitorsMax does, or one user could exhaust logind's fds.
func TestInhibitorsAreCapped(t *testing.T) {
	old := inhibitorsMax
	inhibitorsMax = 3
	t.Cleanup(func() { inhibitorsMax = old })
	r := newInhibitRegistry()
	for i := 0; i < 3; i++ {
		c, _, err := r.add("sleep", "w", "y", "delay", 1000, 1)
		if err != nil {
			t.Fatalf("lock %d: %v", i, err)
		}
		t.Cleanup(func() { c.Close() })
	}
	if _, _, err := r.add("sleep", "w", "y", "delay", 1000, 1); !errors.Is(err, errTooManyInhibitors) {
		t.Fatalf("lock past the cap: err = %v", err)
	}

	m := &manager{inhibitors: r}
	fakeBus(t, map[dbus.Sender]uint32{":1.u": 1000})
	if _, err := m.Inhibit(":1.u", "idle", "w", "y", "block"); err == nil || err.Name != "org.freedesktop.DBus.Error.LimitsExceeded" {
		t.Errorf("Inhibit past the cap: got %v, want LimitsExceeded", err)
	}
}
