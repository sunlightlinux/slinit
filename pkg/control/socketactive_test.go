package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

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
