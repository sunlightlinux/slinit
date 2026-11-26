package main

import (
	"os"
	"testing"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/internal/dbustest"
	"github.com/sunlightlinux/slinit/pkg/dbusservice"
)

// polkitd sends GetUnitByPIDFD a pidfd on every authorization check, and
// nothing closed it: a desktop leaked descriptors in normal use, and any
// user could send them until the bus connection was dropped and login1
// was gone. With the connection set up as main does, each call's pidfd
// is closed once it is answered, for the systemd1 stub and for login1's
// own pidfd call alike.
func TestReceivedPidfdsAreClosed(t *testing.T) {
	usersBus(t)
	server, client := dbustest.Pair(t, dbusservice.CloseReceivedFDs()...)
	s := &systemd1Manager{conn: server, env: map[string]string{}, started: map[string]bool{}}
	if err := server.Export(s, systemd1ObjPath, systemd1Iface); err != nil {
		t.Fatal(err)
	}
	if err := server.Export(&manager{conn: server, inhibitors: newInhibitRegistry()}, objPath, iface); err != nil {
		t.Fatal(err)
	}
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Skipf("no pidfd_open: %v", err)
	}
	defer unix.Close(pidfd)
	sd := client.Object(systemd1BusName, systemd1ObjPath)
	login := client.Object(busName, objPath)
	call := func() {
		t.Helper()
		var p dbus.ObjectPath
		if err := sd.Call(systemd1Iface+".GetUnitByPIDFD", 0, dbus.UnixFD(pidfd)).Store(&p); err == nil {
			t.Fatal("GetUnitByPIDFD answered a unit")
		}
		// No Sender on this bus, so the caller is unknown and refused.
		err := login.Call(iface+".CreateSessionWithPIDFD", 0, uint32(1000), dbus.UnixFD(pidfd),
			"login", "tty", "user", "", "seat0", uint32(1), "tty1", "", false, "", "", uint64(0), []Property{}).Err
		if e, ok := err.(dbus.Error); !ok || e.Name != "org.freedesktop.DBus.Error.AccessDenied" {
			t.Fatalf("CreateSessionWithPIDFD from an unknown caller: %v, want AccessDenied", err)
		}
	}
	call()
	before := openFDCount(t)
	for i := 0; i < 50; i++ {
		call()
	}
	if after := openFDCount(t); after > before {
		t.Errorf("%d descriptors left open after 50 rounds", after-before)
	}
}

func openFDCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(ents)
}
