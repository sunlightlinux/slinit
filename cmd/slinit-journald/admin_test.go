package main

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The admin socket moves the journal between /var and tmpfs, so it must
// not be writable by every local user (it was chmod 0666, and the
// sender was never looked at).
func TestAdminSocketOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.ctl")
	go runAdminSocket(path, &guardedSink{})
	var fi os.FileInfo
	var err error
	for i := 0; i < 200; i++ {
		if fi, err = os.Stat(path); err == nil && fi.Mode().Perm() == 0o600 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("admin socket mode %o, want 600", perm)
	}
}

// Every command arrives with the kernel's view of its sender, and only
// root (or journald's own uid) is let through.
func TestAdminDatagramCarriesSender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.ctl")
	conn, err := listenAdminSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("flush")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	oob := make([]byte, syscall.CmsgSpace(syscall.SizeofUcred))
	n, uid, err := readAdminDatagram(conn, buf, oob)
	if err != nil || string(buf[:n]) != "flush" || uid != uint32(os.Getuid()) {
		t.Fatalf("got %q from uid %d (%v), want flush from %d", buf[:n], uid, err, os.Getuid())
	}
	if !adminSenderAllowed(0) {
		t.Error("root refused")
	}
	other := uint32(os.Geteuid()) + 1
	if other == 0 {
		other = 1
	}
	if adminSenderAllowed(other) {
		t.Errorf("uid %d allowed", other)
	}
}
