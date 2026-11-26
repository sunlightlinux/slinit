package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Between a process's exit and its restart (the restart delay, after a
// crash or the stop half of `slinitctl restart`) the service is STOPPED
// but still wanted STARTED. A stop in that window answered "already
// stopped" without doing anything, and the service came back up
// moments later. dinit's stop always drops the explicit activation
// first. Found by FuzzControlServer (restart, then stop: every service
// STOPPED but one still counted active, and it started again).
func TestStopDuringRestartDelayStopsService(t *testing.T) {
	for _, force := range []bool{false, true} {
		server, sockPath := setupTestServer(t)
		ss := server.services
		svc := service.NewProcessService(ss, "crashy")
		svc.SetCommand([]string{"/bin/sh", "-c", "sleep 0.1; exit 1"})
		svc.SetAutoRestart(service.RestartAlways)
		svc.SetRestartDelay(500 * time.Millisecond)
		svc.SetStopTimeout(time.Second)
		ss.AddService(svc)
		defer func() {
			ss.StopService(svc)
			server.Stop()
		}()

		conn := connectTest(t, sockPath)
		defer conn.Close()
		h := findHandle(t, conn, "crashy")
		if rply, _ := request(t, conn, CmdStartService, EncodeHandle(h)); rply != RplyACK {
			t.Fatalf("start: reply %d", rply)
		}

		// Wait for the first crash: STOPPED, restart pending.
		deadline := time.Now().Add(3 * time.Second)
		for svc.State() != service.StateStopped && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if svc.State() != service.StateStopped {
			t.Fatalf("crashy is %v, want STOPPED in its restart delay", svc.State())
		}

		flags := byte(0)
		if force {
			flags = 0x02
		}
		request(t, conn, CmdStopService, append(EncodeHandle(h), flags))

		time.Sleep(time.Second) // past the restart delay
		if st := svc.State(); st != service.StateStopped {
			t.Errorf("force=%v: stopped in its restart delay, crashy is %v a second later", force, st)
		}
		if n := ss.CountActiveServices(); n != 0 {
			t.Errorf("force=%v: %d services still counted active", force, n)
		}
	}
}
