package service

import (
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOpenPTY(t *testing.T) {
	master, slavePath, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY() failed: %v", err)
	}
	defer master.Close()

	if !strings.HasPrefix(slavePath, "/dev/pts/") {
		t.Errorf("unexpected slave path: %s", slavePath)
	}

	// Verify slave exists
	info, err := os.Stat(slavePath)
	if err != nil {
		t.Fatalf("slave %s not found: %v", slavePath, err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		t.Error("slave should be a character device")
	}
}

func TestVirtualTTY_CreateAndClose(t *testing.T) {
	tmpDir := t.TempDir()

	vt, slavePath, err := OpenVirtualTTY("test-svc", 1024, tmpDir)
	if err != nil {
		t.Fatalf("OpenVirtualTTY failed: %v", err)
	}

	if slavePath == "" {
		t.Error("slavePath should not be empty")
	}

	// Socket should exist
	sockPath := vt.SocketPath()
	if _, err := os.Stat(sockPath); err != nil {
		t.Errorf("vtty socket not found: %v", err)
	}

	// Close should not panic
	vt.Close()

	// Socket should be removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("vtty socket should be removed after close")
	}

	// Double close should not panic
	vt.Close()
}

func TestVirtualTTY_RingBuffer(t *testing.T) {
	tmpDir := t.TempDir()

	vt, slavePath, err := OpenVirtualTTY("ring-svc", 32, tmpDir)
	if err != nil {
		t.Fatalf("OpenVirtualTTY failed: %v", err)
	}
	defer vt.Close()

	// Write to slave side (simulates child process output)
	slave, err := os.OpenFile(slavePath, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave failed: %v", err)
	}

	slave.WriteString("hello from pty\n")
	slave.Close()

	// Give reader goroutine time to process
	time.Sleep(200 * time.Millisecond)

	scrollback := vt.Scrollback()
	if len(scrollback) == 0 {
		t.Error("scrollback should contain data")
	}
	if !strings.Contains(string(scrollback), "hello from pty") {
		t.Errorf("scrollback missing expected content: %q", string(scrollback))
	}
}

func TestVirtualTTY_RingBufferOverflow(t *testing.T) {
	tmpDir := t.TempDir()

	// Small ring buffer
	vt, slavePath, err := OpenVirtualTTY("overflow-svc", 16, tmpDir)
	if err != nil {
		t.Fatalf("OpenVirtualTTY failed: %v", err)
	}
	defer vt.Close()

	slave, err := os.OpenFile(slavePath, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave failed: %v", err)
	}

	// Write more than buffer size
	slave.WriteString("AAAAAAAAAABBBBBBBBBB")
	slave.Close()

	time.Sleep(200 * time.Millisecond)

	scrollback := vt.Scrollback()
	if len(scrollback) > 16 {
		t.Errorf("scrollback should be <= 16 bytes, got %d", len(scrollback))
	}
}

func TestVirtualTTY_ClientAttach(t *testing.T) {
	tmpDir := t.TempDir()

	vt, slavePath, err := OpenVirtualTTY("attach-svc", 4096, tmpDir)
	if err != nil {
		t.Fatalf("OpenVirtualTTY failed: %v", err)
	}
	defer vt.Close()

	// Write some data to slave first (scrollback)
	slave, err := os.OpenFile(slavePath, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave failed: %v", err)
	}
	slave.WriteString("pre-attach output\n")
	time.Sleep(200 * time.Millisecond)

	// Connect a client
	conn, err := net.Dial("unix", vt.SocketPath())
	if err != nil {
		slave.Close()
		t.Fatalf("client connect failed: %v", err)
	}

	// Give accept loop time to register the client
	time.Sleep(100 * time.Millisecond)

	if vt.ClientCount() != 1 {
		t.Errorf("expected 1 client, got %d", vt.ClientCount())
	}

	// Client should receive scrollback
	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		slave.Close()
		conn.Close()
		t.Fatalf("client read failed: %v", err)
	}
	received := string(buf[:n])
	if !strings.Contains(received, "pre-attach output") {
		t.Errorf("expected scrollback, got: %q", received)
	}

	// Write more data — should be forwarded to client
	slave.WriteString("live output\n")
	time.Sleep(200 * time.Millisecond)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = conn.Read(buf)
	if err != nil {
		slave.Close()
		conn.Close()
		t.Fatalf("client read live data failed: %v", err)
	}
	live := string(buf[:n])
	if !strings.Contains(live, "live output") {
		t.Errorf("expected live output, got: %q", live)
	}

	slave.Close()
	conn.Close()
}

func TestVirtualTTY_ClientInput(t *testing.T) {
	tmpDir := t.TempDir()

	vt, slavePath, err := OpenVirtualTTY("input-svc", 4096, tmpDir)
	if err != nil {
		t.Fatalf("OpenVirtualTTY failed: %v", err)
	}
	defer vt.Close()

	// Open slave for reading (simulates child process reading stdin)
	slave, err := os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave failed: %v", err)
	}
	defer slave.Close()

	// Connect client
	conn, err := net.Dial("unix", vt.SocketPath())
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer conn.Close()

	// Client sends input
	conn.Write([]byte("user input\n"))
	time.Sleep(200 * time.Millisecond)

	// Read from slave (what child process would see)
	buf := make([]byte, 256)
	slave.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := slave.Read(buf)
	if err != nil {
		t.Fatalf("slave read failed: %v", err)
	}
	got := string(buf[:n])
	if !strings.Contains(got, "user input") {
		t.Errorf("expected 'user input', got: %q", got)
	}
}

func TestVirtualTTY_MultipleClients(t *testing.T) {
	tmpDir := t.TempDir()

	vt, _, err := OpenVirtualTTY("multi-svc", 4096, tmpDir)
	if err != nil {
		t.Fatalf("OpenVirtualTTY failed: %v", err)
	}
	defer vt.Close()

	// Connect multiple clients
	conn1, err := net.Dial("unix", vt.SocketPath())
	if err != nil {
		t.Fatalf("client 1 connect failed: %v", err)
	}
	defer conn1.Close()

	conn2, err := net.Dial("unix", vt.SocketPath())
	if err != nil {
		t.Fatalf("client 2 connect failed: %v", err)
	}
	defer conn2.Close()

	time.Sleep(100 * time.Millisecond)

	if vt.ClientCount() != 2 {
		t.Errorf("expected 2 clients, got %d", vt.ClientCount())
	}

	// Disconnect one
	conn1.Close()
	time.Sleep(200 * time.Millisecond)
}

