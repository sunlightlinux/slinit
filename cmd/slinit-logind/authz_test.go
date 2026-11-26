package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// fakeBus stands in for the bus daemon's GetConnectionUnixUser: each bus
// name maps to a uid, and an unknown name fails as a vanished peer would.
func fakeBus(t *testing.T, uids map[dbus.Sender]uint32) {
	t.Helper()
	orig := senderUID
	senderUID = func(_ *manager, s dbus.Sender) (uint32, error) {
		if uid, ok := uids[s]; ok {
			return uid, nil
		}
		return 0, errors.New("no such name")
	}
	t.Cleanup(func() { senderUID = orig })
}

// asRoot installs a bus on which ":1.root" is uid 0 and returns that name.
func asRoot(t *testing.T) dbus.Sender {
	t.Helper()
	fakeBus(t, map[dbus.Sender]uint32{":1.root": 0})
	return ":1.root"
}

// withSession writes a session record owned by uid into a temporary state
// directory.
func withSession(t *testing.T, id string, uid uint32) {
	t.Helper()
	orig := stateRoot
	if stateRoot == "/run/slinit-logind" {
		stateRoot = t.TempDir()
		t.Cleanup(func() { stateRoot = orig })
	}
	if err := os.MkdirAll(filepath.Join(stateRoot, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(sessionFile(id), SessionRecord{ID: id, UserID: uid, SeatID: "seat0"}); err != nil {
		t.Fatal(err)
	}
}

const (
	alice dbus.Sender = ":1.alice" // uid 1000, owns session c1
	bob   dbus.Sender = ":1.bob"   // uid 1001
	root  dbus.Sender = ":1.root"
	ghost dbus.Sender = ":1.gone" // not on the bus any more
)

func usersBus(t *testing.T) *manager {
	fakeBus(t, map[dbus.Sender]uint32{alice: 1000, bob: 1001, root: 0})
	withSession(t, "c1", 1000)
	return &manager{inhibitors: newInhibitRegistry()}
}

func denied(err *dbus.Error) bool {
	return err != nil && err.Name == "org.freedesktop.DBus.Error.AccessDenied"
}

// Every call that acts on the machine or on someone else's processes used
// to run for whoever the bus let through. The rules are systemd-logind's
// without polkit: root, or the user the call is about.
func TestAuthorizeRootAndGoodUser(t *testing.T) {
	m := usersBus(t)
	cases := []struct {
		name   string
		sender dbus.Sender
		good   int64
		allow  bool
	}{
		{"root, root-only call", root, noGoodUser, true},
		{"user, root-only call", alice, noGoodUser, false},
		{"good user", alice, 1000, true},
		{"another user", bob, 1000, false},
		{"unidentifiable caller", ghost, 1000, false},
	}
	for _, c := range cases {
		err := m.authorize(c.sender, c.good, "test")
		if c.allow && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
		if !c.allow && !denied(err) {
			t.Errorf("%s: allowed, want AccessDenied (got %v)", c.name, err)
		}
	}
}

// The D-Bus entry points themselves, so a method that forgets its check
// fails here. Each denial is tested from a caller who would otherwise
// succeed in reaching the action; none of these runs the action itself.
func TestMethodsRefuseOtherUsers(t *testing.T) {
	m := usersBus(t)
	so := &sessionObject{m: m, id: "c1"}
	uo := &userObject{m: m, uid: 1000}
	seat := &seatObject{m: m, id: "seat0"}
	mp := &managerProperties{m: m}

	refused := map[string]*dbus.Error{
		"PowerOff":                     m.PowerOff(alice, false),
		"RebootWithFlags":              m.RebootWithFlags(alice, 0),
		"Suspend":                      m.Suspend(alice, false),
		"Sleep":                        m.Sleep(alice, 0),
		"LockSessions":                 m.LockSessions(alice),
		"TerminateSeat":                m.TerminateSeat(alice, "seat0"),
		"SetUserLinger(self)":          m.SetUserLinger(alice, 1000, true, false),
		"KillUser(other)":              m.KillUser(bob, 1000, 15),
		"TerminateUser(other)":         m.TerminateUser(bob, 1000),
		"KillSession(other)":           m.KillSession(bob, "c1", "all", 15),
		"TerminateSession(other)":      m.TerminateSession(bob, "c1"),
		"LockSession(other)":           m.LockSession(bob, "c1"),
		"ReleaseSession(other)":        m.ReleaseSession(bob, "c1"),
		"Session.Kill(other)":          so.Kill(bob, "all", 15),
		"Session.SetLockedHint(other)": so.SetLockedHint(bob, true),
		"Session.TakeControl(other)":   so.TakeControl(bob, false),
		"Session.TakeControl(force)":   so.TakeControl(alice, true),
		"User.Kill(other)":             uo.Kill(bob, 15),
		"Seat.Terminate":               seat.Terminate(alice),
		"Set WallMessage":              mp.Set(alice, "", "WallMessage", dbus.MakeVariant("x")),
	}
	_, _, _, _, _, _, _, _, cerr := m.CreateSession(alice, 1000, 0, "", "", "", "", "", 0, "", "", false, "", "", nil)
	refused["CreateSession"] = cerr
	for name, err := range refused {
		if !denied(err) {
			t.Errorf("%s from a non-root caller: got %v, want AccessDenied", name, err)
		}
	}
}

// The owner keeps what a desktop session needs.
func TestOwnerMayManageOwnSession(t *testing.T) {
	m := usersBus(t)
	so := &sessionObject{m: m, id: "c1"}
	if err := so.SetLockedHint(alice, true); err != nil {
		t.Errorf("owner SetLockedHint: %v", err)
	}
	if err := m.LockSession(alice, "c1"); err != nil {
		t.Errorf("owner LockSession: %v", err)
	}
	if err := so.TakeControl(alice, false); err != nil {
		t.Errorf("owner TakeControl: %v", err)
	}
	if err := so.ReleaseControl(alice); err != nil {
		t.Errorf("controller ReleaseControl: %v", err)
	}
}

// Only the bus name that took control may use the session's devices; the
// owner's other processes may not, nor root's.
func TestDeviceCallsAreTheControllers(t *testing.T) {
	m := usersBus(t)
	resetDevices()
	defer resetDevices()
	so := &sessionObject{m: m, id: "c1"}
	if err := so.TakeControl(alice, false); err != nil {
		t.Fatal(err)
	}
	for _, other := range []dbus.Sender{bob, root, ":1.alice-other-process"} {
		if _, _, err := so.TakeDevice(other, 1, 3); err == nil || err.Name != "org.freedesktop.login1.Error.NotInControl" {
			t.Errorf("TakeDevice from %s: %v, want NotInControl", other, err)
		}
		if err := so.ReleaseControl(other); err == nil {
			t.Errorf("ReleaseControl from %s allowed", other)
		}
	}
	// A forced take by root displaces alice, who is then refused.
	if err := so.TakeControl(root, true); err != nil {
		t.Fatal(err)
	}
	if err := so.ReleaseDevice(alice, 1, 3); err == nil {
		t.Error("displaced controller still allowed")
	}
}

// fixedCapability makes every power and sleep capability probe answer v.
func fixedCapability(t *testing.T, v string) {
	t.Helper()
	ps, pl := probeShutdown, probeSleep
	probeShutdown = func() string { return v }
	probeSleep = func(string) string { return v }
	t.Cleanup(func() { probeShutdown, probeSleep = ps, pl })
}

// A Can* probe must predict its action: a caller the action refuses is
// told "no", not shown a button that answers AccessDenied. That is what
// systemd-logind does without polkit (method_can_shutdown_or_sleep asks
// the action's own authorization question). The actions are only called
// for callers they refuse, so nothing here powers anything off.
func TestCanAnswersMatchTheAction(t *testing.T) {
	m := usersBus(t)
	fixedCapability(t, "yes")
	pairs := []struct {
		name   string
		can    func(dbus.Sender) (string, *dbus.Error)
		action func(dbus.Sender) *dbus.Error
	}{
		{"PowerOff", m.CanPowerOff, func(s dbus.Sender) *dbus.Error { return m.PowerOff(s, false) }},
		{"Reboot", m.CanReboot, func(s dbus.Sender) *dbus.Error { return m.Reboot(s, false) }},
		{"Halt", m.CanHalt, func(s dbus.Sender) *dbus.Error { return m.Halt(s, false) }},
		{"Suspend", m.CanSuspend, func(s dbus.Sender) *dbus.Error { return m.Suspend(s, false) }},
		{"Hibernate", m.CanHibernate, func(s dbus.Sender) *dbus.Error { return m.Hibernate(s, false) }},
		{"HybridSleep", m.CanHybridSleep, func(s dbus.Sender) *dbus.Error { return m.HybridSleep(s, false) }},
		{"SuspendThenHibernate", m.CanSuspendThenHibernate, func(s dbus.Sender) *dbus.Error { return m.SuspendThenHibernate(s, false) }},
		{"Sleep", m.CanSleep, func(s dbus.Sender) *dbus.Error { return m.Sleep(s, 0) }},
	}
	for _, p := range pairs {
		for _, who := range []dbus.Sender{alice, bob, ghost} {
			got, err := p.can(who)
			if err != nil {
				t.Errorf("Can%s(%s): error %v", p.name, who, err)
			}
			if got != "no" {
				t.Errorf("Can%s(%s) = %q, want \"no\"", p.name, who, got)
			}
			if !denied(p.action(who)) {
				t.Errorf("%s(%s) was not refused, yet Can%s said %q", p.name, who, p.name, got)
			}
		}
		if got, _ := p.can(root); got != "yes" {
			t.Errorf("Can%s(root) = %q, want \"yes\"", p.name, got)
		}
	}
}

// An action the machine cannot do is "na" for everyone, root included:
// the capability is decided before the caller is.
func TestCanReportsUnsupportedToEveryone(t *testing.T) {
	m := usersBus(t)
	fixedCapability(t, "na")
	for _, who := range []dbus.Sender{root, alice, ghost} {
		if got, _ := m.CanPowerOff(who); got != "na" {
			t.Errorf("CanPowerOff(%s) = %q, want \"na\"", who, got)
		}
		if got, _ := m.CanHibernate(who); got != "na" {
			t.Errorf("CanHibernate(%s) = %q, want \"na\"", who, got)
		}
	}
}

// Taking the sender is a Go-side change only: godbus fills it in and
// leaves it out of the signature, so clients still see "out s" and no
// arguments. A desktop calling CanPowerOff() with none must still match.
func TestCanSignatureUnchanged(t *testing.T) {
	for _, meth := range introspect.Methods(&manager{}) {
		if !strings.HasPrefix(meth.Name, "Can") {
			continue
		}
		if len(meth.Args) != 1 || meth.Args[0].Direction != "out" || meth.Args[0].Type != "s" {
			t.Errorf("%s: signature %+v, want a single out s", meth.Name, meth.Args)
		}
	}
}

// A non-root ReleaseSession is allowed for the caller's own session,
// found by its pid. A pid reused by another user's process could land in
// a session that is not theirs; the session must also be the caller's
// user's own.
func TestReleaseSessionChecksTheUser(t *testing.T) {
	m := usersBus(t)
	var c1 SessionRecord
	if !readJSON(sessionFile("c1"), &c1) {
		t.Fatal("no c1")
	}
	orig := callerOwnSession
	t.Cleanup(func() { callerOwnSession = orig })
	callerOwnSession = func(*manager, dbus.Sender) (SessionRecord, bool) { return c1, true }

	if err := m.ReleaseSession(bob, "c1"); !denied(err) {
		t.Fatalf("bob, whose pid resolves into alice's c1: %v, want AccessDenied", err)
	}
	if !fileExists(sessionFile("c1")) {
		t.Fatal("c1 was released for bob")
	}
	if err := m.ReleaseSession(alice, "c1"); err != nil {
		t.Fatalf("alice releasing her own c1: %v", err)
	}
	if fileExists(sessionFile("c1")) {
		t.Error("c1 still there after alice released it")
	}
}
