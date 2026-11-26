package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A restart (stop with the restart flag, 0x04) sent once a shutdown has
// begun stopped the service and then started it again with a plain
// start, which never checks for a shutdown: the service came back up,
// explicitly active, with nothing left to stop it, and the shutdown
// waited for it until its timeout. dinit refuses the restart with
// SHUTTINGDOWN (control.cc).
func TestRestartRefusedDuringShutdown(t *testing.T) {
	server, sockPath := setupTestServer(t)
	ss := server.services
	// Slow to stop: it ignores SIGTERM, so it is still STOPPING when the
	// restart comes.
	svc := service.NewProcessService(ss, "slow")
	svc.SetCommand([]string{"/bin/sh", "-c", "trap '' TERM; exec /bin/sleep 3600"})
	svc.SetStopTimeout(time.Second)
	ss.AddService(svc)
	defer server.Stop()

	conn := connectTest(t, sockPath)
	defer conn.Close()
	h := findHandle(t, conn, "slow")
	request(t, conn, CmdStartService, EncodeHandle(h))
	deadline := time.Now().Add(5 * time.Second)
	for svc.State() != service.StateStarted && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the shell set its trap

	ss.StopAllServices(service.ShutdownPoweroff)
	if st := svc.State(); st != service.StateStopping {
		t.Fatalf("service %v, want STOPPING", st)
	}
	if r, _ := request(t, conn, CmdStopService, append(EncodeHandle(h), 0x04)); r != RplyShuttingDown {
		t.Errorf("restart during shutdown answered %d, want RplyShuttingDown (%d)", r, RplyShuttingDown)
	}

	deadline = time.Now().Add(5 * time.Second)
	for svc.State() != service.StateStopped && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := svc.State(); st != service.StateStopped {
		t.Errorf("service %v after the shutdown, want STOPPED", st)
		ss.ForceStopService(svc)
	}
	if n := ss.CountActiveServices(); n != 0 {
		t.Errorf("%d services still counted active", n)
	}
}
