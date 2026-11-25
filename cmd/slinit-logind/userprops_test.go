package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// objectLookup is the part of godbus's default handler the tests use; the
// handler's type is unexported.
type objectLookup interface {
	LookupObject(dbus.ObjectPath) (dbus.ServerObject, bool)
}

// exportBus returns a manager whose connection exports objects into a
// handler the test can look them up in. The far end of the socket is
// drained and nothing answers, which is all exporting and emitting need.
func exportBus(t *testing.T) (*manager, objectLookup) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	ours, far := os.NewFile(uintptr(fds[0]), "ours"), os.NewFile(uintptr(fds[1]), "far")
	go func() {
		buf := make([]byte, 65536)
		for {
			if _, err := far.Read(buf); err != nil {
				return
			}
		}
	}()
	h := dbus.NewDefaultHandler()
	conn, err := dbus.NewConn(ours, dbus.WithHandler(h))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); far.Close() })
	return &manager{conn: conn, inhibitors: newInhibitRegistry()}, h
}

// userProp reads one property of a user object through its exported
// Properties interface, as a client would.
func userProp(t *testing.T, h objectLookup, uid uint32, name string) any {
	t.Helper()
	obj, ok := h.LookupObject(userPath(uid))
	if !ok {
		t.Fatalf("no object for user %d", uid)
	}
	in, ok := obj.LookupInterface("org.freedesktop.DBus.Properties")
	if !ok {
		t.Fatal("user object has no Properties interface")
	}
	meth, ok := in.LookupMethod("Get")
	if !ok {
		t.Fatal("no Properties.Get")
	}
	ifc := userIface
	out, err := meth.Call(&ifc, &name)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	return out[0].(dbus.Variant).Value()
}

// The User object's Sessions and Display were computed once, when the
// user's first session registered it, and never again: a second login
// was missing from Sessions, and after the first one logged out both
// still named it. State was the constant "active".
func TestUserPropertiesFollowSessions(t *testing.T) {
	orig := stateRoot
	stateRoot = t.TempDir()
	t.Cleanup(func() { stateRoot = orig })
	m, h := exportBus(t)

	c1 := SessionRecord{ID: "c1", UserID: 1000, Type: "tty"}
	c2 := SessionRecord{ID: "c2", UserID: 1000, Type: "tty"}
	for _, rec := range []SessionRecord{c1, c2} {
		if err := writeJSONAtomic(sessionFile(rec.ID), rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeJSONAtomic(userFile(1000), UserRecord{UID: 1000}); err != nil {
		t.Fatal(err)
	}
	// c1 is the user's first session, which registers the user object;
	// c2 logs in after and c1 out, as CreateSession and ReleaseSession do.
	m.registerSessionObject(c1)
	m.registerUserObject(UserRecord{UID: 1000})
	m.registerSessionObject(c2)
	sessionMu.Lock()
	m.refreshActiveLocked()
	sessionMu.Unlock()
	if refs, _ := userProp(t, h, 1000, "Sessions").([]SessionRef); len(refs) != 2 {
		t.Errorf("after the second login User.Sessions=%v, want c1 and c2", refs)
	}

	if err := m.releaseSession("c1"); err != nil {
		t.Fatal(err)
	}
	if refs, _ := userProp(t, h, 1000, "Sessions").([]SessionRef); len(refs) != 1 || refs[0].ID != "c2" {
		t.Errorf("after c1 logged out User.Sessions=%v, want [c2]", refs)
	}
	if d, _ := userProp(t, h, 1000, "Display").(SessionRef); d.ID != "c2" {
		t.Errorf("after c1 logged out User.Display=%v, want c2", d)
	}

	// With the foreground VT on none of the user's sessions, the user is
	// online, not active.
	if err := writeJSONAtomic(sessionFile("c2"), SessionRecord{ID: "c2", UserID: 1000, SeatID: "seat0", VTNr: 2}); err != nil {
		t.Fatal(err)
	}
	prevVT := currentVT.Load()
	currentVT.Store(7)
	t.Cleanup(func() { currentVT.Store(prevVT) })
	sessionMu.Lock()
	m.refreshActiveLocked()
	sessionMu.Unlock()
	if s := userProp(t, h, 1000, "State"); s != "online" {
		t.Errorf("User.State=%v with no session of the user in the foreground, want online", s)
	}
}

// User.GID reported the uid.
func TestUserGIDIsThePrimaryGroup(t *testing.T) {
	orig := stateRoot
	stateRoot = t.TempDir()
	t.Cleanup(func() { stateRoot = orig })
	passwd := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwd, []byte("root:x:0:0::/root:/bin/sh\nalice:x:1000:100::/home/alice:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := passwdPath
	passwdPath = passwd
	t.Cleanup(func() { passwdPath = prev })

	m, h := exportBus(t)
	m.registerUserObject(UserRecord{UID: 1000, Name: "alice"})
	if gid := userProp(t, h, 1000, "GID"); gid != uint32(100) {
		t.Errorf("User.GID=%v, want 100 from the passwd entry", gid)
	}
}
