package service

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// socket-permissions used to be applied with os.Chmod on the path after
// bind. The socket's directory is often writable by the service user
// (its runtime-directory), and replacing the socket with a symlink in
// the window between bind and chmod redirected root's chmod to the
// link's target. Measured before the fix: the victim went 0600 -> 0777
// within the first few hundred socket opens.
func TestSocketPermsNotRedirectedBySymlinkRace(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim") // stands in for /etc/shadow
	if err := os.WriteFile(victim, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "svc.sock")

	set, _ := newTestSet()
	s := NewProcessService(set, "sock")
	s.SetSocketDetails(sock, 0o777, -1, -1)

	var stop atomic.Bool
	done := make(chan struct{})
	go func() { // the service user, swapping a symlink in over the socket
		defer close(done)
		tmp := filepath.Join(dir, "tmp")
		for !stop.Load() {
			if fi, err := os.Lstat(sock); err != nil || fi.Mode()&os.ModeSocket == 0 {
				continue
			}
			os.Remove(tmp)
			if os.Symlink(victim, tmp) == nil {
				os.Rename(tmp, sock)
			}
		}
	}()
	deadline := time.Now().Add(500 * time.Millisecond)
	calls := 0
	for ; time.Now().Before(deadline); calls++ {
		os.Remove(sock) // the race needs a fresh bind each round
		if f, err := s.openOneSocket(sock); err == nil {
			f.Close()
		}
		if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
			break
		}
	}
	stop.Store(true)
	<-done
	fi, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("after %d socket opens the symlink target was re-moded to %v; want 0600",
			calls, fi.Mode().Perm())
	}
}

// The ordinary case still applies the requested mode to the socket.
func TestSocketPermsAppliedToSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "svc.sock")
	set, _ := newTestSet()
	s := NewProcessService(set, "sock")
	s.SetSocketDetails(sock, 0o640, -1, -1)
	f, err := s.openOneSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o640 {
		t.Errorf("socket mode = %v; want a socket with 0640", fi.Mode())
	}
}
