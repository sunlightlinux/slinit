package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
)

// The power policy for users: polkit's defaults for org.freedesktop.login1,
// judged on the session of the calling process. These cases build the
// sessions, the foreground VT and the caller's pid by hand, so each
// condition the rule depends on can be broken on its own.

// powerWorld is a machine with sessions on disk, a foreground VT, and bus
// names that map to a uid and a pid.
type powerWorld struct {
	t   *testing.T
	m   *manager
	uid map[dbus.Sender]uint32
	pid map[dbus.Sender]uint32
	ses map[uint32]string // pid -> session id
}

func newPowerWorld(t *testing.T, vt uint32) *powerWorld {
	t.Helper()
	orig := stateRoot
	stateRoot = t.TempDir()
	t.Cleanup(func() { stateRoot = orig })
	if err := os.MkdirAll(filepath.Join(stateRoot, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevVT := currentVT.Load()
	currentVT.Store(vt)
	t.Cleanup(func() { currentVT.Store(prevVT) })

	w := &powerWorld{t: t, m: &manager{inhibitors: newInhibitRegistry()},
		uid: map[dbus.Sender]uint32{}, pid: map[dbus.Sender]uint32{}, ses: map[uint32]string{}}
	ou, op, os_ := senderUID, senderPID, sessionOfPID
	senderUID = func(_ *manager, s dbus.Sender) (uint32, error) {
		if u, ok := w.uid[s]; ok {
			return u, nil
		}
		return 0, errors.New("no such name")
	}
	senderPID = func(_ *manager, s dbus.Sender) (uint32, error) {
		if p, ok := w.pid[s]; ok {
			return p, nil
		}
		return 0, errors.New("no such name")
	}
	sessionOfPID = func(p uint32) string { return w.ses[p] }
	t.Cleanup(func() { senderUID, senderPID, sessionOfPID = ou, op, os_ })
	fixedCapability(t, "yes")
	return w
}

// session writes a session record.
func (w *powerWorld) session(rec SessionRecord) {
	w.t.Helper()
	if err := writeJSONAtomic(sessionFile(rec.ID), rec); err != nil {
		w.t.Fatal(err)
	}
}

// caller registers a bus name for uid whose process (pid) lives in session
// id ("" for none).
func (w *powerWorld) caller(name dbus.Sender, uid, pid uint32, id string) dbus.Sender {
	w.uid[name], w.pid[name] = uid, pid
	if id != "" {
		w.ses[pid] = id
	}
	return name
}

// The user of the foreground session on seat0 may power off, reboot and
// sleep — what a desktop's power menu needs — but not halt, which polkit
// keeps for administrators even in the active session.
func TestActiveLocalUserMayPowerOffButNotHalt(t *testing.T) {
	w := newPowerWorld(t, 2)
	w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2, TTY: "tty2"})
	a := w.caller(":1.desk", 1000, 4242, "c2")

	for _, what := range []string{"poweroff", "reboot", "Suspend", "Hibernate", "HybridSleep", "SuspendThenHibernate", "Sleep"} {
		if err := w.m.mayPower(a, what); err != nil {
			t.Errorf("%s from the active local user: refused: %v", what, err)
		}
	}
	if got, _ := w.m.CanPowerOff(a); got != "yes" {
		t.Errorf("CanPowerOff = %q, want yes", got)
	}
	if got, _ := w.m.CanSuspend(a); got != "yes" {
		t.Errorf("CanSuspend = %q, want yes", got)
	}
	if got, _ := w.m.CanHalt(a); got != "no" {
		t.Errorf("CanHalt = %q, want no", got)
	}
	if !denied(w.m.Halt(a, false)) || !denied(w.m.HaltWithFlags(a, 0)) {
		t.Error("Halt from a user must be refused")
	}
}

// The login screen powers the machine off through the same rule: the
// greeter's session is the active local one.
func TestGreeterMayPowerOff(t *testing.T) {
	w := newPowerWorld(t, 1)
	w.session(SessionRecord{ID: "c1", UserID: 120, SeatID: "seat0", VTNr: 1, TTY: "tty1", Class: "greeter"})
	g := w.caller(":1.gdm", 120, 900, "c1")
	if err := w.m.mayPower(g, "poweroff"); err != nil {
		t.Errorf("greeter refused: %v", err)
	}
}

// Every condition broken on its own is a refusal, for the action and for
// the query that predicts it.
func TestUserPowerRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(w *powerWorld) dbus.Sender
	}{
		{"session in the background", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2})
			w.session(SessionRecord{ID: "c3", UserID: 1001, SeatID: "seat0", VTNr: 3})
			return w.caller(":1.a", 1000, 10, "c2") // VT 3 is in front
		}},
		{"remote session", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c4", UserID: 1000, Remote: true, RemoteHost: "203.0.113.9"})
			return w.caller(":1.a", 1000, 10, "c4")
		}},
		{"remote session that claims a seat", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c3", UserID: 1000, SeatID: "seat0", VTNr: 3, Remote: true})
			return w.caller(":1.a", 1000, 10, "c3")
		}},
		{"session with no seat", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c5", UserID: 1000})
			return w.caller(":1.a", 1000, 10, "c5")
		}},
		// seatFromTTY only assigns seat0 for a ttyN, so a pts session sent
		// with an explicit vtnr is stored seatless but on the foreground VT.
		// The foreground check alone would let it through; the seat check
		// is what stops it.
		{"seatless session claiming the foreground VT", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c6", UserID: 1000, VTNr: 3, TTY: "pts/0"})
			return w.caller(":1.a", 1000, 10, "c6")
		}},
		{"process in no session", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c3", UserID: 1000, SeatID: "seat0", VTNr: 3})
			return w.caller(":1.a", 1000, 10, "")
		}},
		{"process in another user's active session", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c3", UserID: 1001, SeatID: "seat0", VTNr: 3})
			return w.caller(":1.a", 1000, 10, "c3")
		}},
		{"caller pid unknown", func(w *powerWorld) dbus.Sender {
			w.session(SessionRecord{ID: "c3", UserID: 1000, SeatID: "seat0", VTNr: 3})
			w.uid[":1.a"] = 1000
			return ":1.a"
		}},
		{"caller uid unknown", func(w *powerWorld) dbus.Sender {
			return ":1.vanished"
		}},
		{"session file gone", func(w *powerWorld) dbus.Sender {
			return w.caller(":1.a", 1000, 10, "c9")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newPowerWorld(t, 3)
			s := c.setup(w)
			if !denied(w.m.PowerOff(s, false)) {
				t.Error("PowerOff not refused")
			}
			if !denied(w.m.Suspend(s, false)) {
				t.Error("Suspend not refused")
			}
			if got, _ := w.m.CanPowerOff(s); got != "no" {
				t.Errorf("CanPowerOff = %q, want no", got)
			}
		})
	}
}

// With the foreground VT unknown nothing proves the session is in front,
// so the user is refused even though, for display, such a session is
// called active.
func TestUserPowerRefusedWhenForegroundUnknown(t *testing.T) {
	w := newPowerWorld(t, 0)
	w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2})
	a := w.caller(":1.a", 1000, 10, "c2")
	if !sessionIsActive(SessionRecord{SeatID: "seat0", VTNr: 2}) {
		t.Fatal("precondition: with the VT unknown the display rule calls it active")
	}
	if !denied(w.m.PowerOff(a, false)) {
		t.Error("PowerOff granted with the foreground unknown")
	}
}

// A block lock is honoured for users: overriding one is polkit's
// -ignore-inhibit, which is auth_admin_keep. It covers only its class.
func TestUserPowerHonoursBlockInhibitors(t *testing.T) {
	w := newPowerWorld(t, 2)
	w.session(SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2})
	a := w.caller(":1.a", 1000, 10, "c2")

	f, _, err := w.m.inhibitors.add("shutdown", "backup", "copying", "block", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if !denied(w.m.PowerOff(a, false)) || !denied(w.m.Reboot(a, false)) {
		t.Error("power-off/reboot granted over a shutdown block lock")
	}
	if got, _ := w.m.CanPowerOff(a); got != "no" {
		t.Errorf("CanPowerOff over a block lock = %q, want no", got)
	}
	if err := w.m.mayPower(a, "Suspend"); err != nil {
		t.Errorf("a shutdown lock must not block sleep: %v", err)
	}

	g, _, err := w.m.inhibitors.add("sleep", "player", "watching", "delay", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if err := w.m.mayPower(a, "Suspend"); err != nil {
		t.Errorf("a delay lock is not a refusal: %v", err)
	}
}
