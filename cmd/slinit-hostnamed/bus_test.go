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

// The interface as GNOME, NetworkManager and bluetoothd see it, on a real
// bus: the method signatures they call, PropertiesChanged, and polkit
// failing closed when polkitd is not there.
func TestOnABus(t *testing.T) {
	w := newWorld(t)
	addr := dbustest.PrivateBus(t)
	sconn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sconn.Close()
	w.s.svc.Conn = sconn
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
	if err := w.s.svc.Claim(busName); err != nil {
		t.Fatal(err)
	}
	client, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	obj := client.Object(busName, objPath)

	var xml string
	if err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil {
		t.Fatal(err)
	}
	got := dbustest.InSignatures(t, xml, iface)
	for m, want := range map[string]string{
		"SetHostname": "sb", "SetStaticHostname": "sb", "SetPrettyHostname": "sb",
		"SetIconName": "sb", "SetChassis": "sb", "SetDeployment": "sb", "SetLocation": "sb",
		"SetTags": "as", "AddAndRemoveTags": "asas", "GetProductUUID": "b",
		"GetHardwareSerial": "", "Describe": "", "GetMachineInfo": "s",
	} {
		if sig, ok := got[m]; !ok || sig != want {
			t.Errorf("%s signature %q (present %v), want %q", m, sig, ok, want)
		}
	}
	if len(got) != 13 {
		t.Errorf("%d methods exported, want 13: %v", len(got), got)
	}

	if os.Getuid() != 0 {
		call := obj.Call(iface+".SetStaticHostname", 0, "desk", false)
		if e, ok := call.Err.(dbus.Error); !ok || e.Name != "org.freedesktop.DBus.Error.AccessDenied" {
			t.Errorf("without polkitd: %v", call.Err)
		}
	}

	sayYes.Store(true)
	client.AddMatchSignal(dbus.WithMatchInterface(dbusservice.PropIface), dbus.WithMatchMember("PropertiesChanged"))
	sig := make(chan *dbus.Signal, 4)
	client.Signal(sig)
	if call := obj.Call(iface+".SetPrettyHostname", 0, "Desk", true); call.Err != nil {
		t.Fatal(call.Err)
	}
	select {
	case s := <-sig:
		if ch := s.Body[1].(map[string]dbus.Variant); ch["PrettyHostname"].Value() != "Desk" {
			t.Errorf("PropertiesChanged %v", s.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no PropertiesChanged for PrettyHostname")
	}
	var v dbus.Variant
	if err := obj.Call(dbusservice.PropIface+".Get", 0, iface, "Chassis").Store(&v); err != nil {
		t.Errorf("Get Chassis: %v", err)
	}
}
