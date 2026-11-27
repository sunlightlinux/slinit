package control

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// slinitctl add-dep and rm-dep accept `prepared-by`, and slinitctl(8)
// documents it, but the server took only the dependency types up to
// `after` and answered every prepared-by request BADREQ, so the command
// always failed with "reply 52".
func TestAddRmDepPreparedBy(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services
	from := service.NewInternalService(ss, "pb-from")
	to := service.NewInternalService(ss, "pb-to")
	ss.AddService(from)
	ss.AddService(to)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	req := EncodeDepRequest(findHandle(t, conn, "pb-from"), findHandle(t, conn, "pb-to"),
		uint8(service.DepPreparedBy))

	if rply, _ := request(t, conn, CmdAddDep, req); rply != RplyACK {
		t.Fatalf("add-dep prepared-by: reply %d, want ACK", rply)
	}
	deps := from.Record().Dependencies()
	if len(deps) != 1 || deps[0].To != to || deps[0].DepType != service.DepPreparedBy {
		t.Fatalf("edges after add-dep: %v", deps)
	}
	if rply, _ := request(t, conn, CmdRmDep, req); rply != RplyACK {
		t.Fatalf("rm-dep prepared-by: reply %d, want ACK", rply)
	}
	if n := len(from.Record().Dependencies()); n != 0 {
		t.Fatalf("%d edges left after rm-dep", n)
	}

	// A type past prepared-by names nothing.
	bad := EncodeDepRequest(findHandle(t, conn, "pb-from"), findHandle(t, conn, "pb-to"),
		uint8(service.DepPreparedBy)+1)
	if rply, _ := request(t, conn, CmdAddDep, bad); rply != RplyBadReq {
		t.Fatalf("add-dep type %d: reply %d, want BADREQ", service.DepPreparedBy+1, rply)
	}
}
