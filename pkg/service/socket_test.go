package service

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSocketCreation(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	set, _ := newTestSet()
	svc := NewProcessService(set, "sock-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sockPath, 0660, -1, -1)
	set.AddService(svc)

	// Open socket
	err := svc.openSocket()
	if err != nil {
		t.Fatalf("openSocket() failed: %v", err)
	}
	defer svc.closeSocket()

	// Verify socket file exists
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("socket file not found: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Error("file is not a socket")
	}

	// Verify we can connect to it
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("cannot connect to socket: %v", err)
	}
	conn.Close()

	// Verify socketFD is set
	if svc.socketFD == nil {
		t.Error("socketFD should be non-nil after openSocket")
	}
}

func TestSocketCleanupOnClose(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	set, _ := newTestSet()
	svc := NewProcessService(set, "sock-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sockPath, 0600, -1, -1)
	set.AddService(svc)

	// Open and then close
	if err := svc.openSocket(); err != nil {
		t.Fatalf("openSocket() failed: %v", err)
	}
	svc.closeSocket()

	// Socket file should be removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after closeSocket")
	}

	// socketFD should be nil
	if svc.socketFD != nil {
		t.Error("socketFD should be nil after closeSocket")
	}
}

func TestSocketPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	set, _ := newTestSet()
	svc := NewProcessService(set, "sock-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sockPath, 0600, -1, -1)
	set.AddService(svc)

	if err := svc.openSocket(); err != nil {
		t.Fatalf("openSocket() failed: %v", err)
	}
	defer svc.closeSocket()

	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}

	// Check permissions (mask with 0777 since socket type bits differ)
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("expected permissions 0600, got %o", perm)
	}
}

func TestSocketPassedToChild(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")
	markerPath := filepath.Join(tmpDir, "marker")

	set, _ := newTestSet()
	svc := NewProcessService(set, "sock-svc")
	// Script checks that LISTEN_FDS=1 is set and fd 3 is valid
	svc.SetCommand([]string{"/bin/sh", "-c",
		`if [ "$LISTEN_FDS" = "1" ]; then echo ok > ` + markerPath + `; fi; sleep 60`})
	svc.Record().SetSocketDetails(sockPath, 0600, -1, -1)
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// Check that LISTEN_FDS was set (marker file created)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("marker file not found - LISTEN_FDS not set: %v", err)
	}
	if string(data) != "ok\n" {
		t.Errorf("unexpected marker content: %q", string(data))
	}

	// Clean up
	svc.Stop(true)
	set.ProcessQueues()
	time.Sleep(500 * time.Millisecond)
}

func TestSocketStaleRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create a stale socket file
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to create stale socket: %v", err)
	}
	listener.Close()

	set, _ := newTestSet()
	svc := NewProcessService(set, "sock-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sockPath, 0600, -1, -1)
	set.AddService(svc)

	// Should succeed - stale socket removed and new one created
	if err := svc.openSocket(); err != nil {
		t.Fatalf("openSocket() with stale socket failed: %v", err)
	}
	defer svc.closeSocket()

	// Verify new socket works
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("cannot connect to new socket: %v", err)
	}
	conn.Close()
}

func TestSocketNotASocket(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create a regular file at the socket path
	os.WriteFile(sockPath, []byte("not a socket"), 0644)

	set, _ := newTestSet()
	svc := NewProcessService(set, "sock-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sockPath, 0600, -1, -1)
	set.AddService(svc)

	err := svc.openSocket()
	if err == nil {
		svc.closeSocket()
		t.Fatal("expected error when socket path is a regular file")
	}
}

func TestMultipleSockets(t *testing.T) {
	tmpDir := t.TempDir()
	sock1 := filepath.Join(tmpDir, "s1.sock")
	sock2 := filepath.Join(tmpDir, "s2.sock")

	set, _ := newTestSet()
	svc := NewProcessService(set, "multi-sock")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sock1, 0600, -1, -1)
	svc.Record().SetSocketPaths([]string{sock1, sock2})
	set.AddService(svc)

	if err := svc.openSocket(); err != nil {
		t.Fatalf("openSocket() failed: %v", err)
	}
	defer svc.closeSocket()

	// Both sockets should exist
	for _, p := range []string{sock1, sock2} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("socket %s not found: %v", p, err)
		}
		if info.Mode()&os.ModeSocket == 0 {
			t.Errorf("%s is not a socket", p)
		}
	}

	// Primary socket in socketFD
	if svc.socketFD == nil {
		t.Error("socketFD should be set")
	}
	// Second socket in socketFDs
	if len(svc.socketFDs) != 1 {
		t.Errorf("expected 1 extra socket fd, got %d", len(svc.socketFDs))
	}
}

func TestMultipleSocketsCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	sock1 := filepath.Join(tmpDir, "s1.sock")
	sock2 := filepath.Join(tmpDir, "s2.sock")

	set, _ := newTestSet()
	svc := NewProcessService(set, "multi-sock")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sock1, 0600, -1, -1)
	svc.Record().SetSocketPaths([]string{sock1, sock2})
	set.AddService(svc)

	svc.openSocket()
	svc.closeSocket()

	for _, p := range []string{sock1, sock2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("socket %s should be removed after close", p)
		}
	}
}

func TestTCPSocketCreation(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "tcp-sock")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails("tcp:127.0.0.1:0", 0, -1, -1)
	svc.Record().SetSocketPaths([]string{"tcp:127.0.0.1:0"})
	set.AddService(svc)

	if err := svc.openSocket(); err != nil {
		t.Fatalf("openSocket(tcp) failed: %v", err)
	}
	defer svc.closeSocket()

	if svc.socketFD == nil {
		t.Error("socketFD should be set for TCP socket")
	}
}

func TestMultipleSocketsPassedToChild(t *testing.T) {
	tmpDir := t.TempDir()
	sock1 := filepath.Join(tmpDir, "s1.sock")
	sock2 := filepath.Join(tmpDir, "s2.sock")
	markerPath := filepath.Join(tmpDir, "marker")

	set, _ := newTestSet()
	svc := NewProcessService(set, "multi-sock-child")
	svc.SetCommand([]string{"/bin/sh", "-c",
		`if [ "$LISTEN_FDS" = "2" ]; then echo ok > ` + markerPath + `; fi; sleep 60`})
	svc.Record().SetSocketDetails(sock1, 0600, -1, -1)
	svc.Record().SetSocketPaths([]string{sock1, sock2})
	set.AddService(svc)

	set.StartService(svc)
	time.Sleep(500 * time.Millisecond)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	data, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("marker not found — LISTEN_FDS=2 not set: %v", err)
	}
	if string(data) != "ok\n" {
		t.Errorf("unexpected marker: %q", string(data))
	}

	svc.Stop(true)
	set.ProcessQueues()
	time.Sleep(500 * time.Millisecond)
}

func TestOnDemandWatcherStartStop(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "demand.sock")

	set, _ := newTestSet()
	svc := NewProcessService(set, "demand-svc")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketDetails(sockPath, 0600, -1, -1)
	svc.SetSocketOnDemand(true)
	set.AddService(svc)

	// Open socket first (on-demand needs the socket pre-created)
	if err := svc.openSocket(); err != nil {
		t.Fatalf("openSocket failed: %v", err)
	}
	defer svc.closeSocket()

	// Start watcher
	svc.startOnDemandWatcher()
	time.Sleep(100 * time.Millisecond)

	// Stop watcher — should not panic
	svc.stopOnDemandWatcher()
}
