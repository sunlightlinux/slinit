package main

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// elogindManagerProps is the property list from elogind's
// org.freedesktop.login1.Manager vtable (src/login/logind-dbus.c,
// manager_vtable), name and signature verbatim. Divergence from this
// list is what breaks `loginctl show` and the desktop power stacks, so
// it's pinned here rather than left to review.
var elogindManagerProps = map[string]string{
	"EnableWallMessages":               "b",
	"WallMessage":                      "s",
	"NAutoVTs":                         "u",
	"KillOnlyUsers":                    "as",
	"KillExcludeUsers":                 "as",
	"KillUserProcesses":                "b",
	"RebootParameter":                  "s",
	"RebootToFirmwareSetup":            "b",
	"RebootToBootLoaderMenu":           "t",
	"RebootToBootLoaderEntry":          "s",
	"BootLoaderEntries":                "as",
	"IdleHint":                         "b",
	"IdleSinceHint":                    "t",
	"IdleSinceHintMonotonic":           "t",
	"BlockInhibited":                   "s",
	"BlockWeakInhibited":               "s",
	"DelayInhibited":                   "s",
	"InhibitDelayMaxUSec":              "t",
	"UserStopDelayUSec":                "t",
	"SleepOperation":                   "as",
	"HandlePowerKey":                   "s",
	"HandlePowerKeyLongPress":          "s",
	"HandleRebootKey":                  "s",
	"HandleRebootKeyLongPress":         "s",
	"HandleSuspendKey":                 "s",
	"HandleSuspendKeyLongPress":        "s",
	"HandleHibernateKey":               "s",
	"HandleHibernateKeyLongPress":      "s",
	"HandleLidSwitch":                  "s",
	"HandleLidSwitchExternalPower":     "s",
	"HandleLidSwitchDocked":            "s",
	"HandleSecureAttentionKey":         "s",
	"HoldoffTimeoutUSec":               "t",
	"IdleAction":                       "s",
	"IdleActionUSec":                   "t",
	"PreparingForShutdown":             "b",
	"PreparingForShutdownWithMetadata": "a{sv}",
	"PreparingForSleep":                "b",
	"ScheduledShutdown":                "(st)",
	"DesignatedMaintenanceTime":        "s",
	"Docked":                           "b",
	"LidClosed":                        "b",
	"OnExternalPower":                  "b",
	"RemoveIPC":                        "b",
	"RuntimeDirectorySize":             "t",
	"RuntimeDirectoryInodesMax":        "t",
	"InhibitorsMax":                    "t",
	"NCurrentInhibitors":               "t",
	"SessionsMax":                      "t",
	"NCurrentSessions":                 "t",
	"UserTasksMax":                     "t",
	"StopIdleSessionUSec":              "t",
}

// TestManagerPropCoverage pins our property set to elogind's: no
// missing names, no extras, same declared signatures.
func TestManagerPropCoverage(t *testing.T) {
	ours := map[string]string{}
	for _, mp := range managerPropSpec {
		if _, dup := ours[mp.Name]; dup {
			t.Errorf("duplicate property %q in managerPropSpec", mp.Name)
		}
		ours[mp.Name] = mp.Sig
	}

	for name, sig := range elogindManagerProps {
		got, ok := ours[name]
		if !ok {
			t.Errorf("missing property %q (elogind declares %q)", name, sig)
			continue
		}
		if got != sig {
			t.Errorf("property %q: signature %q, elogind declares %q", name, got, sig)
		}
	}
	for name := range ours {
		if _, ok := elogindManagerProps[name]; !ok {
			t.Errorf("property %q is not in elogind's Manager vtable", name)
		}
	}
}

// TestManagerPropSignatures checks each getter actually marshals to the
// signature the table declares. A Go `int` where the table says "t", or
// an anonymous struct where it says "(st)", produces a value loginctl
// rejects at parse time — and without a live bus that only shows up on
// the target machine.
func TestManagerPropSignatures(t *testing.T) {
	m := &manager{}
	for _, mp := range managerPropSpec {
		v := mp.Get(m)
		if v == nil {
			t.Errorf("%s: getter returned nil", mp.Name)
			continue
		}
		got := dbus.SignatureOf(v).String()
		if got != mp.Sig {
			t.Errorf("%s: getter marshals as %q, table declares %q", mp.Name, got, mp.Sig)
		}
	}
}

// TestManagerPropertiesGetAll covers the empty-interface call loginctl
// makes (`GetAll("")`), the explicit one, and rejection of a foreign
// interface.
func TestManagerPropertiesGetAll(t *testing.T) {
	p := &managerProperties{m: &manager{}}

	for _, name := range []string{"", iface} {
		all, err := p.GetAll(name)
		if err != nil {
			t.Fatalf("GetAll(%q): %v", name, err)
		}
		if len(all) != len(managerPropSpec) {
			t.Errorf("GetAll(%q): %d properties, want %d", name, len(all), len(managerPropSpec))
		}
		if _, ok := all["NCurrentSessions"]; !ok {
			t.Errorf("GetAll(%q): NCurrentSessions absent", name)
		}
	}

	if _, err := p.GetAll("org.example.Other"); err == nil {
		t.Error("GetAll on a foreign interface should fail")
	}
}

func TestManagerPropertiesGet(t *testing.T) {
	p := &managerProperties{m: &manager{}}

	v, err := p.Get("", "SessionsMax")
	if err != nil {
		t.Fatalf("Get(SessionsMax): %v", err)
	}
	if got := v.Value().(uint64); got != 8192 {
		t.Errorf("SessionsMax = %d, want 8192", got)
	}

	if _, err := p.Get("", "NoSuchProperty"); err == nil {
		t.Error("Get of an unknown property should fail")
	}
}

// TestManagerPropertiesSet covers the two writable properties and the
// read-only rejection every other one must give.
func TestManagerPropertiesSet(t *testing.T) {
	m := &manager{}
	p := &managerProperties{m: m}

	if err := p.Set("", "WallMessage", dbus.MakeVariant("system going down")); err != nil {
		t.Fatalf("Set(WallMessage): %v", err)
	}
	if m.wallMessage != "system going down" {
		t.Errorf("wallMessage = %q, want %q", m.wallMessage, "system going down")
	}
	v, _ := p.Get("", "WallMessage")
	if got := v.Value().(string); got != "system going down" {
		t.Errorf("WallMessage reads back %q", got)
	}

	if err := p.Set("", "EnableWallMessages", dbus.MakeVariant(true)); err != nil {
		t.Fatalf("Set(EnableWallMessages): %v", err)
	}
	if !m.enableWallMessages {
		t.Error("enableWallMessages not stored")
	}

	// Wrong type must not clobber the stored value.
	if err := p.Set("", "WallMessage", dbus.MakeVariant(uint32(7))); err == nil {
		t.Error("Set(WallMessage) with a uint32 should fail")
	}
	if m.wallMessage != "system going down" {
		t.Errorf("failed Set clobbered wallMessage to %q", m.wallMessage)
	}

	if err := p.Set("", "SessionsMax", dbus.MakeVariant(uint64(1))); err != prop.ErrReadOnly {
		t.Errorf("Set on a read-only property returned %v, want ErrReadOnly", err)
	}
}

// TestManagerIntrospectProps checks the XML the desktop stacks discover
// us with: one entry per property, correct access mode, and an
// EmitsChangedSignal annotation on every entry.
func TestManagerIntrospectProps(t *testing.T) {
	props := managerIntrospectProps()
	if len(props) != len(managerPropSpec) {
		t.Fatalf("%d introspection entries, want %d", len(props), len(managerPropSpec))
	}
	writable := map[string]bool{"EnableWallMessages": true, "WallMessage": true}
	for _, ip := range props {
		want := "read"
		if writable[ip.Name] {
			want = "readwrite"
		}
		if ip.Access != want {
			t.Errorf("%s: access %q, want %q", ip.Name, ip.Access, want)
		}
		if len(ip.Annotations) != 1 ||
			ip.Annotations[0].Name != "org.freedesktop.DBus.Property.EmitsChangedSignal" {
			t.Errorf("%s: missing EmitsChangedSignal annotation", ip.Name)
			continue
		}
		switch ip.Annotations[0].Value {
		case "const", "true", "false":
		default:
			t.Errorf("%s: EmitsChangedSignal = %q", ip.Name, ip.Annotations[0].Value)
		}
	}
}

func TestRuntimeDirectorySize(t *testing.T) {
	// 10% of RAM on any machine that can run the test suite is well
	// above the 1 MiB floor, and the inodes divisor must not truncate
	// to zero.
	if got := runtimeDirectorySize(); got < 1024*1024 {
		t.Errorf("runtimeDirectorySize() = %d, implausibly small", got)
	}
}

// TestSleepOperations guards the ordering contract: Manager.Sleep picks
// the first entry, and it writes "mem", so "suspend" must never trail
// "hibernate".
func TestSleepOperations(t *testing.T) {
	ops := sleepOperations()
	for i, op := range ops {
		switch op {
		case "suspend":
			if i != 0 {
				t.Errorf("suspend at index %d, want 0 (%v)", i, ops)
			}
		case "hibernate":
		default:
			t.Errorf("unexpected sleep operation %q — Manager.Sleep only writes /sys/power/state", op)
		}
	}
}
