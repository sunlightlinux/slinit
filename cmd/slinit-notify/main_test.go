package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// manager stands in for the service manager: it reads every datagram,
// closes the descriptors one carries (which answers a barrier), and
// hands on the messages other than BARRIER=1.
type manager struct {
	msgs     chan string
	barriers chan struct{}
}

func listen(t *testing.T, name string) *manager {
	t.Helper()
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	m := &manager{msgs: make(chan string, 16), barriers: make(chan struct{}, 16)}
	go func() {
		buf, oob := make([]byte, 4096), make([]byte, 256)
		for {
			n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
			if err != nil {
				return
			}
			if scms, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, s := range scms {
					fds, _ := syscall.ParseUnixRights(&s)
					for _, fd := range fds {
						syscall.Close(fd)
					}
				}
			}
			if string(buf[:n]) == "BARRIER=1" {
				m.barriers <- struct{}{}
				continue
			}
			m.msgs <- string(buf[:n])
		}
	}()
	return m
}

func recv(t *testing.T, m *manager) string {
	t.Helper()
	select {
	case s := <-m.msgs:
		return s
	case <-time.After(2 * time.Second):
		t.Fatal("no message")
	}
	return ""
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

// The barrier: by default slinit-notify waits until the manager has
// closed the descriptor sent after the message; --no-block does not
// send one; a manager that never answers is an error after the timeout.
func TestNotifyBarrier(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "n.sock")
	m := listen(t, sock)
	var out, errb bytes.Buffer
	if rc := run([]string{"--ready"}, sock, &out, &errb); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	recv(t, m)
	select {
	case <-m.barriers:
	case <-time.After(time.Second):
		t.Fatal("no BARRIER=1 sent")
	}
	if rc := run([]string{"--no-block", "--ready"}, sock, &out, &errb); rc != 0 {
		t.Fatalf("--no-block: exit %d", rc)
	}
	recv(t, m)
	select {
	case <-m.barriers:
		t.Error("--no-block sent a barrier")
	case <-time.After(200 * time.Millisecond):
	}

	// A manager that reads nothing: the barrier times out.
	old := barrierTimeout
	barrierTimeout = 200 * time.Millisecond
	defer func() { barrierTimeout = old }()
	mute := filepath.Join(t.TempDir(), "mute.sock")
	c, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: mute, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	errb.Reset()
	if rc := run([]string{"--ready"}, mute, &out, &errb); rc == 0 {
		t.Error("an unanswered barrier exited 0")
	}
}
