package main

import (
	"bufio"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"

	"github.com/sunlightlinux/slinit/pkg/polkit"
)

// privateBus starts a dbus-daemon of its own and returns its address.
func privateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not installed")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "bus.conf")
	os.WriteFile(conf, []byte(`<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=`+dir+`</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow send_destination="*"/>
    <allow receive_sender="*"/>
    <allow own="*"/>
  </policy>
</busconfig>`), 0o644)
	cmd := exec.Command("dbus-daemon", "--config-file="+conf, "--nofork", "--print-address=1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("dbus-daemon address: %v", err)
	}
	return strings.TrimSpace(line)
}

// The interface as a desktop sees it, on a real bus: method signatures,
// properties, PropertiesChanged, and polkit failing closed when polkitd
// is not there.
func TestOnABus(t *testing.T) {
	w := newWorld(t)
	addr := privateBus(t)

	sconn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer sconn.Close()
	w.s.conn = sconn
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
	if err := export(sconn, w.s); err != nil {
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
	want := map[string]string{"SetTime": "xbb", "SetTimezone": "sb", "SetLocalRTC": "bbb", "SetNTP": "bb", "ListTimezones": ""}
	for _, ifc := range parseIntrospect(t, xml) {
		if ifc.Name != iface {
			continue
		}
		for _, m := range ifc.Methods {
			in := ""
			for _, a := range m.Args {
				if a.Direction == "in" {
					in += a.Type
				}
			}
			if w, ok := want[m.Name]; ok && w != in {
				t.Errorf("%s signature %q, want %q", m.Name, in, w)
			}
			delete(want, m.Name)
		}
		if len(ifc.Properties) != 7 {
			t.Errorf("%d properties introspected", len(ifc.Properties))
		}
	}
	if len(want) != 0 {
		t.Errorf("methods missing from introspection: %v", want)
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
	client.AddMatchSignal(dbus.WithMatchInterface(propIface), dbus.WithMatchMember("PropertiesChanged"))
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
	if err := obj.Call(propIface+".Get", 0, iface, "Timezone").Store(&tz); err != nil || tz.Value() != "Europe/Paris" {
		t.Errorf("Get Timezone: %v %v", tz, err)
	}
}

func parseIntrospect(t *testing.T, xml string) []introspect.Interface {
	t.Helper()
	var n introspect.Node
	if err := xmlUnmarshal(xml, &n); err != nil {
		t.Fatal(err)
	}
	return n.Interfaces
}

func xmlUnmarshal(s string, v any) error { return xml.Unmarshal([]byte(s), v) }
