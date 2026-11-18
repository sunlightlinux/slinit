package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/sunlightlinux/slinit/internal/dbustest"
	"github.com/sunlightlinux/slinit/pkg/dbusservice"
	"github.com/sunlightlinux/slinit/pkg/polkit"
)

// The interface as a desktop sees it, on a real bus: method signatures,
// properties, PropertiesChanged, and polkit failing closed when polkitd
// is not there.
func TestOnABus(t *testing.T) {
	w := newWorld(t)
	addr := dbustest.PrivateBus(t)

	sconn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sconn.Close()
	w.s.svc.Conn = sconn
	// The real polkit check first (there is no polkitd on this bus), then
	// a yes. One Checker, switched by a flag, so the test never writes a
	// field the server's handler goroutines read.
	real := polkit.New(sconn)
	var sayYes atomic.Bool
	w.s.pk = &polkit.Checker{
		UID: real.UID,
		Check: func(s dbus.Sender, a string, d map[string]string, i bool) (bool, bool, error) {
			if sayYes.Load() {
				return true, false, nil
			}
			return real.Check(s, a, d, i)
		},
	}
	if err := w.s.svc.Export(w.s); err != nil {
		t.Fatal(err)
	}
	if r, err := sconn.RequestName(busName, dbus.NameFlagDoNotQueue); err != nil || r != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("RequestName: %v %v", r, err)
	}

	client, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	obj := client.Object(busName, objPath)

	// GNOME calls these exact signatures; the sender argument must not
	// leak into them.
	var xml string
	if err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil {
		t.Fatal(err)
	}
	got := dbustest.InSignatures(t, xml, iface)
	for m, w := range map[string]string{"SetTime": "xbb", "SetTimezone": "sb", "SetLocalRTC": "bbb", "SetNTP": "bb", "ListTimezones": ""} {
		if sig, ok := got[m]; !ok || sig != w {
			t.Errorf("%s signature %q (present %v), want %q", m, sig, ok, w)
		}
	}

	// No polkitd: a non-root caller is refused, and nothing changes.
	if os.Getuid() != 0 {
		call := obj.Call(iface+".SetTimezone", 0, "Europe/Paris", false)
		if e, ok := call.Err.(dbus.Error); !ok || e.Name != "org.freedesktop.DBus.Error.AccessDenied" {
			t.Errorf("without polkitd: %v", call.Err)
		}
	}

	// With polkit answering yes, the change lands and is announced.
	sayYes.Store(true)
	client.AddMatchSignal(dbus.WithMatchInterface(dbusservice.PropIface), dbus.WithMatchMember("PropertiesChanged"))
	sig := make(chan *dbus.Signal, 4)
	client.Signal(sig)
	if call := obj.Call(iface+".SetTimezone", 0, "Europe/Paris", true); call.Err != nil {
		t.Fatal(call.Err)
	}
	select {
	case s := <-sig:
		changed := s.Body[1].(map[string]dbus.Variant)
		if s.Body[0] != iface || changed["Timezone"].Value() != "Europe/Paris" {
			t.Errorf("PropertiesChanged %v", s.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no PropertiesChanged for Timezone")
	}
	var tz dbus.Variant
	if err := obj.Call(dbusservice.PropIface+".Get", 0, iface, "Timezone").Store(&tz); err != nil || tz.Value() != "Europe/Paris" {
		t.Errorf("Get Timezone: %v %v", tz, err)
	}
}
