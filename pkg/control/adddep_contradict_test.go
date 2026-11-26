package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A hard dependency added to a running service on a target that is not
// STARTED contradicts itself: the dependent is up without what it
// depends on. dinit refuses it (NAK). slinit installed the edge and
// asked the target to start; a target that failed left the dependent
// STARTED on a STOPPED hard dependency still counted as required, and
// one that waited (a triggered service) left it STARTED on a STARTING
// one. Found by FuzzControlServer.
func TestAddHardDepToRunningServiceNeedsStartedTarget(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services

	a := service.NewInternalService(ss, "running")
	fail := service.NewScriptedService(ss, "fails")
	fail.SetStartCommand([]string{"/bin/false"})
	trig := service.NewTriggeredService(ss, "waits")
	up := service.NewInternalService(ss, "up")
	for _, s := range []service.Service{a, fail, trig, up} {
		ss.AddService(s)
	}
	ss.StartService(a)
	ss.StartService(up)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	ha := findHandle(t, conn, "running")

	for _, tc := range []struct {
		target  string
		depType service.DependencyType
	}{
		{"fails", service.DepRegular},
		{"waits", service.DepRegular},
	} {
		ht := findHandle(t, conn, tc.target)
		rply, _ := request(t, conn, CmdAddDep, EncodeDepRequest(ha, ht, uint8(tc.depType)))
		time.Sleep(200 * time.Millisecond) // let a start the target triggered fail
		if rply != RplyNAK {
			t.Errorf("add-dep running -%v-> %s (%v): reply %d, want NAK; running is %v",
				tc.depType, tc.target, ss.FindService(tc.target, false).State(), rply, a.State())
		}
	}
	for _, d := range a.Record().Dependencies() {
		t.Errorf("edge running -> %s (%v) installed", d.To.Name(), d.DepType)
	}
	if n := fail.Record().RequiredBy(); n != 0 {
		t.Errorf("fails is %v and still required by %d", fail.State(), n)
	}

	// A started target, a soft edge, or a stopped dependent are fine.
	hu := findHandle(t, conn, "up")
	if rply, _ := request(t, conn, CmdAddDep, EncodeDepRequest(ha, hu, uint8(service.DepRegular))); rply != RplyACK {
		t.Errorf("add-dep running -> up (STARTED): reply %d, want ACK", rply)
	}
	hw := findHandle(t, conn, "waits")
	if rply, _ := request(t, conn, CmdAddDep, EncodeDepRequest(ha, hw, uint8(service.DepWaitsFor))); rply != RplyACK {
		t.Errorf("add-dep running waits-for waits: reply %d, want ACK", rply)
	}
	hf := findHandle(t, conn, "fails")
	if rply, _ := request(t, conn, CmdAddDep, EncodeDepRequest(hf, hw, uint8(service.DepRegular))); rply != RplyACK {
		t.Errorf("add-dep fails (STOPPED) -> waits: reply %d, want ACK", rply)
	}
}
