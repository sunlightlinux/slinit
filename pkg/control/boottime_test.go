package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

func TestBootTimeEncodeDecode(t *testing.T) {
	now := time.Now()
	info := BootTimeInfo{
		KernelUptimeNs: int64(5 * time.Second),
		BootStartNs:    now.UnixNano(),
		BootReadyNs:    now.Add(500 * time.Millisecond).UnixNano(),
		BootSvcName:    "boot",
		Services: []BootTimeEntry{
			{Name: "hello", StartupNs: int64(234 * time.Millisecond), State: service.StateStarted, SvcType: service.TypeScripted, PID: 0},
			{Name: "ticker", StartupNs: int64(456 * time.Millisecond), State: service.StateStarted, SvcType: service.TypeProcess, PID: 129},
		},
	}

	encoded := EncodeBootTime(info)
	decoded, err := DecodeBootTime(encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}

	if decoded.KernelUptimeNs != info.KernelUptimeNs {
		t.Errorf("KernelUptime mismatch: got %d, want %d", decoded.KernelUptimeNs, info.KernelUptimeNs)
	}
	if decoded.BootStartNs != info.BootStartNs {
		t.Errorf("BootStart mismatch: got %d, want %d", decoded.BootStartNs, info.BootStartNs)
	}
	if decoded.BootReadyNs != info.BootReadyNs {
		t.Errorf("BootReady mismatch: got %d, want %d", decoded.BootReadyNs, info.BootReadyNs)
	}
	if decoded.BootSvcName != info.BootSvcName {
		t.Errorf("BootSvcName mismatch: got %q, want %q", decoded.BootSvcName, info.BootSvcName)
	}
	if len(decoded.Services) != 2 {
		t.Fatalf("Expected 2 services, got %d", len(decoded.Services))
	}

	if decoded.Services[0].Name != "hello" {
		t.Errorf("First service name: got %q, want %q", decoded.Services[0].Name, "hello")
	}
	if decoded.Services[0].StartupNs != int64(234*time.Millisecond) {
		t.Errorf("First service startup: got %d, want %d", decoded.Services[0].StartupNs, int64(234*time.Millisecond))
	}
	if decoded.Services[0].State != service.StateStarted {
		t.Errorf("First service state: got %d, want %d", decoded.Services[0].State, service.StateStarted)
	}
	if decoded.Services[0].SvcType != service.TypeScripted {
		t.Errorf("First service type: got %d, want %d", decoded.Services[0].SvcType, service.TypeScripted)
	}

	if decoded.Services[1].Name != "ticker" {
		t.Errorf("Second service name: got %q, want %q", decoded.Services[1].Name, "ticker")
	}
	if decoded.Services[1].PID != 129 {
		t.Errorf("Second service PID: got %d, want 129", decoded.Services[1].PID)
	}
}

func TestBootTimeEncodeDecodeEmpty(t *testing.T) {
	info := BootTimeInfo{
		KernelUptimeNs: int64(2 * time.Second),
		BootStartNs:    time.Now().UnixNano(),
		BootReadyNs:    0, // not ready yet
		BootSvcName:    "boot",
		Services:       nil,
	}

	encoded := EncodeBootTime(info)
	decoded, err := DecodeBootTime(encoded)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}

	if decoded.BootReadyNs != 0 {
		t.Errorf("BootReady should be 0, got %d", decoded.BootReadyNs)
	}
	if len(decoded.Services) != 0 {
		t.Errorf("Expected 0 services, got %d", len(decoded.Services))
	}
}

func TestBootTimeCommand(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	server.services.SetBootStartTime(time.Now().Add(-500 * time.Millisecond))
	server.services.SetBootServiceName("boot")
	server.services.SetKernelUptime(2 * time.Second)

	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdBootTime, nil); err != nil {
		t.Fatalf("Write error: %v", err)
	}

	rply, payload, err := ReadPacket(conn)
	if err != nil {
		t.Fatalf("Read error: %v", err)
	}
	if rply != RplyBootTime {
		t.Fatalf("Expected RplyBootTime(%d), got %d", RplyBootTime, rply)
	}

	info, err := DecodeBootTime(payload)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}

	if info.BootSvcName != "boot" {
		t.Errorf("Expected boot service name 'boot', got %q", info.BootSvcName)
	}
	if info.KernelUptimeNs != int64(2*time.Second) {
		t.Errorf("Kernel uptime mismatch: got %d, want %d", info.KernelUptimeNs, int64(2*time.Second))
	}
}

// TestBootTimeSoftRebootTail round-trips the trailing extension that
// carries soft-reboot bookkeeping.
func TestBootTimeSoftRebootTail(t *testing.T) {
	info := BootTimeInfo{
		KernelUptimeNs: int64(550 * time.Millisecond),
		BootSvcName:    "boot",
		SoftReboots:    3,
		StartUptimeNs:  int64(22180 * time.Millisecond),
		Services: []BootTimeEntry{
			{Name: "hello", StartupNs: int64(12 * time.Millisecond), State: service.StateStarted},
		},
	}

	decoded, err := DecodeBootTime(EncodeBootTime(info))
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if decoded.SoftReboots != 3 {
		t.Errorf("SoftReboots: got %d, want 3", decoded.SoftReboots)
	}
	if decoded.StartUptimeNs != info.StartUptimeNs {
		t.Errorf("StartUptimeNs: got %d, want %d", decoded.StartUptimeNs, info.StartUptimeNs)
	}
	// The tail must not disturb what came before it.
	if decoded.KernelUptimeNs != info.KernelUptimeNs {
		t.Errorf("KernelUptimeNs: got %d, want %d", decoded.KernelUptimeNs, info.KernelUptimeNs)
	}
	if len(decoded.Services) != 1 || decoded.Services[0].Name != "hello" {
		t.Errorf("service array damaged by the tail: %+v", decoded.Services)
	}
}

// TestBootTimeTailAbsent covers a newer slinitctl talking to a daemon
// that predates the tail: the payload simply ends after the service
// array, which must decode cleanly and report a plain boot rather than
// failing or inventing a soft-reboot count.
func TestBootTimeTailAbsent(t *testing.T) {
	info := BootTimeInfo{
		KernelUptimeNs: int64(550 * time.Millisecond),
		BootSvcName:    "boot",
		SoftReboots:    3,
		StartUptimeNs:  int64(22180 * time.Millisecond),
		Services: []BootTimeEntry{
			{Name: "hello", StartupNs: int64(12 * time.Millisecond), State: service.StateStarted},
		},
	}

	full := EncodeBootTime(info)
	old := full[:len(full)-bootTimeTailLen]

	decoded, err := DecodeBootTime(old)
	if err != nil {
		t.Fatalf("Decode of a tail-less payload must succeed, got: %v", err)
	}
	if decoded.SoftReboots != 0 {
		t.Errorf("SoftReboots: got %d, want 0 for a daemon without the tail", decoded.SoftReboots)
	}
	if decoded.StartUptimeNs != 0 {
		t.Errorf("StartUptimeNs: got %d, want 0", decoded.StartUptimeNs)
	}
	if len(decoded.Services) != 1 || decoded.Services[0].Name != "hello" {
		t.Errorf("services lost when the tail is absent: %+v", decoded.Services)
	}
}
