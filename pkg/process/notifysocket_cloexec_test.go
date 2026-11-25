package process

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fdCapture struct{ ch chan []*os.File }

func (c fdCapture) OnNotify(_ NotifyMessage, fds []*os.File) { c.ch <- fds }

// An fd a service stores with FDSTORE=1 lands in PID 1. Received
// without MSG_CMSG_CLOEXEC it was inheritable, so every child slinit
// started afterwards (any service, any user) got a copy of it. It must
// arrive close-on-exec and still reach the service it belongs to
// through the fd-store hand-off.
func TestNotifySocketReceivedFDsAreCloexec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "n.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	l := &NotifySocketListener{path: path, conn: conn, stopCh: make(chan struct{})}
	got := fdCapture{ch: make(chan []*os.File, 1)}
	l.Start(got)
	defer l.Stop()

	stored, err := os.Create(filepath.Join(dir, "stored"))
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()

	cl, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(cl)
	if err := unix.Sendmsg(cl, []byte("FDSTORE=1\nFDNAME=x\n"),
		unix.UnixRights(int(stored.Fd())), &unix.SockaddrUnix{Name: path}, 0); err != nil {
		t.Fatal(err)
	}
	var fds []*os.File
	select {
	case fds = <-got.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no notify message delivered")
	}
	if len(fds) != 1 {
		t.Fatalf("got %d fds, want 1", len(fds))
	}
	defer fds[0].Close()

	flags, err := unix.FcntlInt(fds[0].Fd(), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("received fd %d is not close-on-exec: it leaks into every later child", fds[0].Fd())
	}

	// The hand-off: os/exec dup2s the stored fd into slot 3, which
	// clears close-on-exec on the copy the service gets.
	out := filepath.Join(dir, "out")
	_, ch, err := StartProcess(ExecParams{
		Command:   []string{"/bin/sh", "-c", "readlink /proc/self/fd/3 > " + out},
		StoredFDs: []FDStoreEntry{{Name: "x", File: fds[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	b, _ := os.ReadFile(out)
	if strings.TrimSpace(string(b)) != stored.Name() {
		t.Errorf("service fd 3 = %q, want the stored file %q", strings.TrimSpace(string(b)), stored.Name())
	}
}
