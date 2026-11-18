// Package dbustest starts a private dbus-daemon for tests of slinit's D-Bus
// services, so they can be exercised on a real bus without the system one.
package dbustest

import (
	"bufio"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5/introspect"
)

// PrivateBus starts a dbus-daemon of its own for the test and returns its
// address; the test is skipped where dbus-daemon is not installed. The
// policy allows everything, receive_sender included: without that rule the
// daemon refuses to deliver replies, and Hello never returns.
func PrivateBus(t *testing.T) string {
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

// InSignatures returns each method of iface with its input signature, from
// introspection XML.
func InSignatures(t *testing.T, xmlData, iface string) map[string]string {
	t.Helper()
	var n introspect.Node
	if err := xml.Unmarshal([]byte(xmlData), &n); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, i := range n.Interfaces {
		if i.Name != iface {
			continue
		}
		for _, m := range i.Methods {
			in := ""
			for _, a := range m.Args {
				if a.Direction == "in" {
					in += a.Type
				}
			}
			out[m.Name] = in
		}
	}
	return out
}
