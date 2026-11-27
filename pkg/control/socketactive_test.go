package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// A second instance must not take over the socket of one that is
// running: removing it would leave the running instance unreachable.
func TestStartRefusesLiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slinit.socket")
	running, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	go func() {
		for {
			c, err := running.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	if !SocketActive(path) {
		t.Fatal("SocketActive on a listening socket = false, want true")
	}

	srv := NewServer(nil, path, logging.New(logging.LevelError))
	err = srv.Start(context.Background())
	if err == nil {
		srv.Stop()
		t.Fatal("Start beside a running instance succeeded, want ErrSocketActive")
	}
	if !errors.Is(err, ErrSocketActive) {
		t.Errorf("Start = %v, want ErrSocketActive", err)
	}

	// The running instance must still be reachable through its socket.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the running instance's socket file is gone: %v", err)
	}
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("the running instance no longer answers: %v", err)
	}
	c.Close()
}

// A socket file left by an instance that died is stale: it does not count
// as active, and a new instance replaces it as before.
func TestStartReplacesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slinit.socket")
	dead, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	dead.SetUnlinkOnClose(false) // leave the file behind, as a crash would
	dead.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("setup: stale socket file missing: %v", err)
	}

	if SocketActive(path) {
		t.Fatal("SocketActive on a stale socket file = true, want false")
	}
	srv := NewServer(nil, path, logging.New(logging.LevelError))
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start over a stale socket: %v", err)
	}
	defer srv.Stop()
	if !SocketActive(path) {
		t.Error("after Start, the new instance's socket does not answer")
	}
}

// Reopen (SIGUSR1) must not take over another instance's live socket
// either, but must recreate its own when the file is gone.
func TestReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slinit.socket")
	srv := NewServer(nil, path, logging.New(logging.LevelError))
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	// The file deleted from under us -- what `slinit -m` used to do.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if SocketActive(path) {
		t.Fatal("setup: socket still answers after its file was removed")
	}
	if err := srv.Reopen(); err != nil {
		t.Fatalf("Reopen after the file was removed: %v", err)
	}
	if !SocketActive(path) {
		t.Fatal("Reopen did not bring the socket back")
	}
}

// A control socket that could not be bound at boot (/run full: a local
// user can exhaust its inodes) is recreated by SIGUSR1 once there is
// room. The first connection to it then killed PID 1: Start had
// returned before recording its context, and serve read a nil one.
func TestReopenAfterFailedStart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	path := filepath.Join(dir, "slinit.socket")
	srv := NewServer(nil, path, logging.New(logging.LevelError))
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("setup: Start succeeded with no directory to bind in")
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := srv.Reopen(); err != nil {
		t.Fatalf("Reopen once there is room: %v", err)
	}
	defer srv.Stop()

	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial the reopened socket: %v", err)
	}
	defer c.Close()
	if err := WritePacket(c, CmdQueryVersion, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if rply, _, err := ReadPacket(c); err != nil || rply != RplyCPVersion {
		t.Fatalf("version query on the reopened socket: reply %d, %v", rply, err)
	}
}

// pass-cs-fd hands a service a control connection whether or not the
// socket file could be bound; with the bind failed, serving it took the
// same nil context.
func TestPassCSFDAfterFailedStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "slinit.socket")
	srv := NewServer(nil, path, logging.New(logging.LevelError))
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("setup: Start succeeded with no directory to bind in")
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) net.Conn {
		f := os.NewFile(uintptr(fd), "cs")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ours, theirs := conn(fds[0]), conn(fds[1])
	defer theirs.Close()
	srv.HandlePassCSFD(ours)
	defer srv.Stop()

	theirs.SetDeadline(time.Now().Add(5 * time.Second))
	if err := WritePacket(theirs, CmdQueryVersion, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	if rply, _, err := ReadPacket(theirs); err != nil || rply != RplyCPVersion {
		t.Fatalf("version query on the pass-cs-fd connection: reply %d, %v", rply, err)
	}
}
