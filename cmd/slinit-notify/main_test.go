package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func listen(t *testing.T, name string) *net.UnixConn {
	t.Helper()
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func recv(t *testing.T, c *net.UnixConn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestNotifyMessages(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "n.sock")
	c := listen(t, sock)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--ready"}, "READY=1"},
		{[]string{"--ready", "--status=up and running"}, "READY=1\nSTATUS=up and running"},
		{[]string{"WATCHDOG=1"}, "WATCHDOG=1"},
		{[]string{"--stopping", "--reloading"}, "STOPPING=1\nRELOADING=1"},
		{[]string{"--pid=1234"}, "MAINPID=1234"},
		{[]string{"--pid=self"}, "MAINPID=" + strconv.Itoa(os.Getpid())},
	} {
		var out, errb bytes.Buffer
		if rc := run(tc.args, sock, &out, &errb); rc != 0 {
			t.Fatalf("%v: exit %d: %s", tc.args, rc, errb.String())
		}
		if got := recv(t, c); got != tc.want {
			t.Errorf("%v: sent %q, want %q", tc.args, got, tc.want)
		}
	}
}

// An abstract socket, written "@name" in $NOTIFY_SOCKET as systemd
// writes it.
func TestNotifyAbstractSocket(t *testing.T) {
	name := "slinit-notify-test-" + strconv.Itoa(os.Getpid())
	c := listen(t, "\x00"+name)
	var out, errb bytes.Buffer
	if rc := run([]string{"--ready"}, "@"+name, &out, &errb); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	if got := recv(t, c); got != "READY=1" {
		t.Errorf("sent %q", got)
	}
}

func TestNotifyFailures(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "n.sock")
	listen(t, sock)
	for _, tc := range []struct {
		args   []string
		socket string
	}{
		{[]string{"--ready"}, ""},              // no $NOTIFY_SOCKET
		{[]string{"--ready"}, "relative.sock"}, // neither a path nor abstract
		{nil, sock},                            // nothing to send
		{[]string{"--frobnicate"}, sock},       // unknown option
		{[]string{"NOEQUALS"}, sock},           // not VARIABLE=VALUE
		{[]string{"--pid=0"}, sock},            // invalid pid
		{[]string{"--booted"}, sock},           // not systemd
	} {
		var out, errb bytes.Buffer
		if rc := run(tc.args, tc.socket, &out, &errb); rc == 0 {
			t.Errorf("%v with socket %q: exit 0, want a failure", tc.args, tc.socket)
		}
	}
}
