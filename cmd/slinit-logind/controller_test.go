package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func nameOwnerChanged(name, oldOwner, newOwner string) *dbus.Signal {
	return &dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{name, oldOwner, newOwner}}
}

// Only a unique name leaving the bus means a process went away.
func TestVanishedName(t *testing.T) {
	cases := []struct {
		sig  *dbus.Signal
		want string
	}{
		{nameOwnerChanged(":1.77", ":1.77", ""), ":1.77"},
		{nameOwnerChanged(":1.77", "", ":1.77"), ""},             // appeared
		{nameOwnerChanged("org.kde.KWin", ":1.77", ""), ""},      // well-known name released
		{nameOwnerChanged("org.kde.KWin", ":1.77", ":1.78"), ""}, // changed hands
		{&dbus.Signal{Name: "org.freedesktop.DBus.NameLost", Body: []any{":1.77"}}, ""},
		{&dbus.Signal{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{":1.77"}}, ""},
		{nil, ""},
	}
	for i, c := range cases {
		if got := vanishedName(c.sig); got != c.want {
			t.Errorf("case %d: vanishedName = %q, want %q", i, got, c.want)
		}
	}
}

// Found with Plasma's Wayland session on sl-demo1: the first kwin_wayland
// took control and died, and every kwin_wayland its wrapper started after
// it was refused DeviceIsTaken, because control stayed with the dead bus
// name. Losing the controller's name must free the session for the next.
func TestControlFreedWhenControllerLeavesTheBus(t *testing.T) {
	devicesMu.Lock()
	devices = map[string]*sessionDevices{}
	devicesMu.Unlock()
	t.Cleanup(func() { devicesMu.Lock(); devices = map[string]*sessionDevices{}; devicesMu.Unlock() })

	if err := takeControl("c13", ":1.77", false); err != nil {
		t.Fatal(err)
	}
	if err := takeControl("c13", ":1.78", false); err == nil || err.Name != "org.freedesktop.login1.Error.DeviceIsTaken" {
		t.Fatalf("second compositor while the first lives: %v, want DeviceIsTaken", err)
	}
	if err := takeControl("c20", ":1.90", false); err != nil {
		t.Fatal(err)
	}

	if ids := dropController(":1.77"); len(ids) != 1 || ids[0] != "c13" {
		t.Fatalf("dropController(:1.77) released %v, want [c13]", ids)
	}
	if err := takeControl("c13", ":1.78", false); err != nil {
		t.Errorf("after the first compositor left the bus, TakeControl: %v", err)
	}
	if !isController("c20", ":1.90") {
		t.Error("another session's controller was released too")
	}
	if ids := dropController(":1.99"); len(ids) != 0 {
		t.Errorf("an unrelated name released %v", ids)
	}
}
