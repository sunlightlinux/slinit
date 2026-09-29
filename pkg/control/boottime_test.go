package control

import (
	"encoding/binary"
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
	old := full[:len(full)-bootTimeTailLen-bootTimeStampsLen(len(info.Services))]

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

// TestBootTimeStampTail round-trips the per-service start instants that
// `analyze plot` lays out. They ride in a parallel array after the
// soft-reboot tail, so this also pins down that the two tails do not
// overwrite each other.
func TestBootTimeStampTail(t *testing.T) {
	base := time.Now().UnixNano()
	info := BootTimeInfo{
		BootStartNs: base,
		BootReadyNs: base + int64(900*time.Millisecond),
		BootSvcName: "boot",
		SoftReboots: 2,
		Services: []BootTimeEntry{
			{Name: "early", StartReqNs: base, StartedNs: base + int64(30*time.Millisecond)},
			// Still starting: a request instant but no started instant.
			{Name: "slow", StartReqNs: base + int64(40*time.Millisecond)},
			// Never asked to start: both zero, and must stay zero.
			{Name: "idle"},
		},
	}

	decoded, err := DecodeBootTime(EncodeBootTime(info))
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if decoded.SoftReboots != 2 {
		t.Errorf("SoftReboots damaged by the second tail: got %d, want 2", decoded.SoftReboots)
	}
	if len(decoded.Services) != 3 {
		t.Fatalf("Expected 3 services, got %d", len(decoded.Services))
	}
	for i, want := range info.Services {
		got := decoded.Services[i]
		if got.StartReqNs != want.StartReqNs || got.StartedNs != want.StartedNs {
			t.Errorf("service %q: got req=%d started=%d, want req=%d started=%d",
				want.Name, got.StartReqNs, got.StartedNs, want.StartReqNs, want.StartedNs)
		}
	}
}

// TestBootTimeStampTailAbsent covers a slinitctl that knows about the
// timestamps talking to a daemon that does not send them: the payload
// ends after the soft-reboot tail. Everything before must survive and
// the instants must read as zero rather than as garbage picked up from
// off the end of the buffer.
func TestBootTimeStampTailAbsent(t *testing.T) {
	info := BootTimeInfo{
		KernelUptimeNs: int64(550 * time.Millisecond),
		BootSvcName:    "boot",
		SoftReboots:    1,
		StartUptimeNs:  int64(9 * time.Second),
		Services: []BootTimeEntry{
			{Name: "hello", StartupNs: int64(12 * time.Millisecond),
				StartReqNs: 1234, StartedNs: 5678},
		},
	}

	full := EncodeBootTime(info)
	old := full[:len(full)-bootTimeStampsLen(len(info.Services))]

	decoded, err := DecodeBootTime(old)
	if err != nil {
		t.Fatalf("Decode without the stamp tail must succeed, got: %v", err)
	}
	if decoded.SoftReboots != 1 || decoded.StartUptimeNs != info.StartUptimeNs {
		t.Errorf("soft-reboot tail lost: %+v", decoded)
	}
	if len(decoded.Services) != 1 {
		t.Fatalf("Expected 1 service, got %d", len(decoded.Services))
	}
	if decoded.Services[0].StartReqNs != 0 || decoded.Services[0].StartedNs != 0 {
		t.Errorf("instants should be zero without the tail, got req=%d started=%d",
			decoded.Services[0].StartReqNs, decoded.Services[0].StartedNs)
	}
	if decoded.Services[0].StartupNs != int64(12*time.Millisecond) {
		t.Errorf("duration lost: got %d", decoded.Services[0].StartupNs)
	}
}

// TestBootTimeStampCountMismatch: a stamp array that does not line up
// with the service array describes different services, so it is dropped
// whole. Pairing them positionally anyway would attach one service's
// timing to another's name — a plot that looks right and is wrong.
func TestBootTimeStampCountMismatch(t *testing.T) {
	info := BootTimeInfo{
		BootSvcName: "boot",
		Services: []BootTimeEntry{
			{Name: "a", StartReqNs: 100, StartedNs: 200},
			{Name: "b", StartReqNs: 300, StartedNs: 400},
		},
	}
	buf := EncodeBootTime(info)
	// Rewrite the stamp count to 1 while leaving two pairs in place.
	countOff := len(buf) - bootTimeStampsLen(2)
	binary.LittleEndian.PutUint16(buf[countOff:], 1)

	decoded, err := DecodeBootTime(buf)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	for _, s := range decoded.Services {
		if s.StartReqNs != 0 || s.StartedNs != 0 {
			t.Errorf("service %q took instants from a mismatched array: req=%d started=%d",
				s.Name, s.StartReqNs, s.StartedNs)
		}
	}
}

// TestBootTimeCommandStamps proves the daemon actually fills the
// instants in — the encoder being correct says nothing about whether
// handleBootTime reads the record's clocks.
func TestBootTimeCommandStamps(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	// Drive the real state machine rather than stuffing the fields: the
	// question is whether handleBootTime reads the clocks the daemon
	// actually keeps.
	done := service.NewInternalService(server.services, "timed")
	server.services.AddService(done)
	done.Start()
	server.services.ProcessQueues()
	if done.State() != service.StateStarted {
		t.Fatalf("setup: expected STARTED, got %d", done.State())
	}

	// A service held in STARTING by an unstarted dependency: it has a
	// request instant and no started instant, which is the case the plot
	// draws as an open-ended bar.
	blocker := service.NewInternalService(server.services, "blocker")
	server.services.AddService(blocker)
	pending := service.NewInternalService(server.services, "pending")
	server.services.AddService(pending)
	pending.Record().AddDep(blocker, service.DepRegular)
	pending.Start()
	if pending.State() != service.StateStarting {
		t.Fatalf("setup: expected STARTING, got %d", pending.State())
	}

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

	seen := map[string]BootTimeEntry{}
	for _, s := range info.Services {
		seen[s.Name] = s
	}

	got, ok := seen["timed"]
	if !ok {
		t.Fatalf("service 'timed' missing from the reply")
	}
	if got.StartReqNs == 0 || got.StartedNs == 0 {
		t.Errorf("daemon sent no instants: req=%d started=%d", got.StartReqNs, got.StartedNs)
	}
	if got.StartedNs < got.StartReqNs {
		t.Errorf("started (%d) precedes the request (%d)", got.StartedNs, got.StartReqNs)
	}

	got, ok = seen["pending"]
	if !ok {
		t.Fatalf("service 'pending' missing from the reply")
	}
	if got.StartReqNs == 0 {
		t.Errorf("a STARTING service must report when it was asked to start")
	}
	if got.StartedNs != 0 {
		t.Errorf("a STARTING service must not report a started instant, got %d", got.StartedNs)
	}
}
