package control

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// enable and disable read the target's enable-via while a reload of that
// service rewrites it under the graph lock (config.applyToService runs
// inside reload-all's WithGraphLock). They used to read it without the
// lock: enable-via is a string, so the read could tear, and -race reported
// it in a stress run of the daemon. This test gives the detector that
// interleaving.
func TestEnableDisableReadEnableViaUnderReload(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	ss := server.services
	boot := service.NewInternalService(ss, "boot")
	target := service.NewInternalService(ss, "ev-target")
	ss.AddService(boot)
	ss.AddService(target)
	ss.SetBootServiceName("boot")

	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := findHandle(t, conn, "ev-target")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			ss.WithGraphLock(func() { target.Record().SetEnableVia("") })
		}
	}()

	for i := 0; i < 50; i++ {
		WritePacket(conn, CmdEnableService, EncodeHandle(handle))
		readReply(t, conn)
		WritePacket(conn, CmdDisableService, EncodeHandle(handle))
		readReply(t, conn)
	}
	<-done
}
