// Package dbustest starts a private dbus-daemon for tests of slinit's D-Bus
// services, so they can be exercised on a real bus without the system one.
package dbustest

import (
	"bufio"
	"encoding/xml"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

// PrivateBus starts a dbus-daemon of its own for the test and returns its
// address; the test is skipped where dbus-daemon is not installed. The
// policy allows everything, receive_sender included: without that rule the
// daemon refuses to deliver replies, and Hello never returns.
func PrivateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon not on PATH (the reference daemon; dbus-broker cannot stand in, its launcher needs a systemd manager)")
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

// Pair returns two connections joined by a stand-in for the bus, for
// tests that need what a private dbus-daemon cannot be relied on for:
// descriptors passed between them, on a host without dbus-daemon. The
// stand-in does the SASL handshake (EXTERNAL, with descriptor passing)
// and then relays bytes and SCM_RIGHTS between the two; it routes
// nothing and adds no Sender, and neither side sends Hello, so a call
// from client reaches server whatever destination it names. server is
// made with opts.
func Pair(t *testing.T, opts ...dbus.ConnOption) (server, client *dbus.Conn) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bus")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	accepted := make(chan *net.UnixConn, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := l.AcceptUnix()
			if err != nil {
				close(accepted)
				return
			}
			if err := saslServer(c); err != nil {
				c.Close()
				close(accepted)
				return
			}
			accepted <- c
		}
	}()
	dial := func(o ...dbus.ConnOption) *dbus.Conn {
		c, err := dbus.Dial("unix:path="+path, o...)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Auth(nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	server = dial(opts...)
	a := <-accepted
	client = dial()
	b := <-accepted
	if a == nil || b == nil {
		t.Fatal("dbustest: handshake failed")
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	go relay(a, b)
	go relay(b, a)
	return server, client
}

// saslServer answers a godbus client's handshake. It reads a byte at a
// time, so nothing after BEGIN is consumed here, descriptors included.
func saslServer(c *net.UnixConn) error {
	one := make([]byte, 1)
	if _, err := io.ReadFull(c, one); err != nil { // the credentials byte
		return err
	}
	for {
		var line []byte
		for !strings.HasSuffix(string(line), "\r\n") {
			if _, err := io.ReadFull(c, one); err != nil {
				return err
			}
			line = append(line, one[0])
		}
		var reply string
		switch cmd := strings.TrimSpace(string(line)); {
		case cmd == "AUTH":
			reply = "REJECTED EXTERNAL"
		case strings.HasPrefix(cmd, "AUTH EXTERNAL"):
			reply = "OK 0123456789abcdef0123456789abcdef"
		case cmd == "NEGOTIATE_UNIX_FD":
			reply = "AGREE_UNIX_FD"
		case cmd == "BEGIN":
			return nil
		default:
			reply = "ERROR"
		}
		if _, err := c.Write([]byte(reply + "\r\n")); err != nil {
			return err
		}
	}
}

// relay copies from one side to the other, descriptors included, and
// closes its own copies of the descriptors once they are passed on.
func relay(from, to *net.UnixConn) {
	buf := make([]byte, 64<<10)
	oob := make([]byte, syscall.CmsgSpace(253*4))
	for {
		n, oobn, _, _, err := from.ReadMsgUnix(buf, oob)
		if err != nil {
			to.Close()
			return
		}
		_, _, err = to.WriteMsgUnix(buf[:n], oob[:oobn], nil)
		if scms, perr := syscall.ParseSocketControlMessage(oob[:oobn]); perr == nil {
			for i := range scms {
				fds, _ := syscall.ParseUnixRights(&scms[i])
				for _, fd := range fds {
					syscall.Close(fd)
				}
			}
		}
		if err != nil {
			from.Close()
			return
		}
	}
}
