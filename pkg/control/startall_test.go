package control

import (
	"encoding/binary"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// startAll issues CmdStartAll and returns the started/skipped summary.
func startAll(t *testing.T, sockPath string) (started, skipped uint16) {
	t.Helper()
	conn := connectTest(t, sockPath)
	defer conn.Close()

	if err := WritePacket(conn, CmdStartAll, nil); err != nil {
		t.Fatalf("writing CmdStartAll: %v", err)
	}
	rply, payload := readReply(t, conn)
	if rply != RplyStartAllResult {
		t.Fatalf("reply = %d, want RplyStartAllResult (%d)", rply, RplyStartAllResult)
	}
	if len(payload) < 4 {
		t.Fatalf("short reply: %d bytes", len(payload))
	}
	return binary.LittleEndian.Uint16(payload[0:2]), binary.LittleEndian.Uint16(payload[2:4])
}

func TestStartAllStartsStoppedServices(t *testing.T) {
	server, sockPath := setupTestServer(t)
	ss := server.services

	for _, n := range []string{"one", "two", "three"} {
		ss.AddService(service.NewInternalService(ss, n))
	}

	started, skipped := startAll(t, sockPath)
	if started != 3 {
		t.Errorf("started = %d, want 3", started)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}

	ss.ProcessQueues()
	for _, n := range []string{"one", "two", "three"} {
		svc := ss.FindService(n, false)
		if svc == nil || svc.State() != service.StateStarted {
			t.Errorf("%s did not start", n)
		}
	}
}

// Running services are counted as skipped, not started again.
func TestStartAllSkipsAlreadyStarted(t *testing.T) {
	server, sockPath := setupTestServer(t)
	ss := server.services

	up := service.NewInternalService(ss, "up")
	ss.AddService(up)
	ss.StartService(up)
	ss.ProcessQueues()
	if up.State() != service.StateStarted {
		t.Fatalf("precondition: up is %v", up.State())
	}

	down := service.NewInternalService(ss, "down")
	ss.AddService(down)

	started, skipped := startAll(t, sockPath)
	if started != 1 {
		t.Errorf("started = %d, want 1 (only the stopped one)", started)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the running one)", skipped)
	}
}

// manual=yes is documented as refusing every activation path except an
// explicit `slinitctl start <service>`. A bulk start is not that, so it
// must not be a way around the directive.
func TestStartAllSkipsManualStart(t *testing.T) {
	server, sockPath := setupTestServer(t)
	ss := server.services

	manual := service.NewInternalService(ss, "manual-only")
	manual.Record().SetManualStart(true)
	ss.AddService(manual)

	started, skipped := startAll(t, sockPath)
	if started != 0 {
		t.Errorf("started = %d, want 0 — manual=yes must not be started in bulk", started)
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}

	ss.ProcessQueues()
	if manual.State() == service.StateStarted {
		t.Error("a manual=yes service was started by start-all")
	}
}

// refuse-manual-start answers RplyManualRefused on the per-service path;
// the bulk path has no business being more permissive.
func TestStartAllSkipsRefuseManualStart(t *testing.T) {
	server, sockPath := setupTestServer(t)
	ss := server.services

	svc := service.NewInternalService(ss, "refuses")
	svc.Record().SetRefuseManualStart(true)
	ss.AddService(svc)

	started, skipped := startAll(t, sockPath)
	if started != 0 || skipped != 1 {
		t.Errorf("started/skipped = %d/%d, want 0/1", started, skipped)
	}
	ss.ProcessQueues()
	if svc.State() == service.StateStarted {
		t.Error("a refuse-manual-start service was started by start-all")
	}
}

// A stop pin is the operator's recorded intent and outranks a sweep.
func TestStartAllSkipsStopPinned(t *testing.T) {
	server, sockPath := setupTestServer(t)
	ss := server.services

	svc := service.NewInternalService(ss, "pinned")
	ss.AddService(svc)
	svc.Record().PinStop()

	started, skipped := startAll(t, sockPath)
	if started != 0 || skipped != 1 {
		t.Errorf("started/skipped = %d/%d, want 0/1", started, skipped)
	}
	ss.ProcessQueues()
	if svc.State() == service.StateStarted {
		t.Error("a stop-pinned service was started by start-all")
	}
}
