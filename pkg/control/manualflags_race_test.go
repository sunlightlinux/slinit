package control

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// start, stop and start-all gate on manual-start / refuse-manual-start /
// refuse-manual-stop, which a reload of the service rewrites under the
// graph lock (config.applyToService inside reload-all's WithGraphLock).
// They read them without the lock; -race reported it in a stress run of
// the daemon. This test gives the detector that interleaving.
func TestStartStopReadManualFlagsUnderReload(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	ss := server.services
	target := service.NewInternalService(ss, "mf-target")
	ss.AddService(target)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := findHandle(t, conn, "mf-target")

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			ss.WithGraphLock(func() {
				r := target.Record()
				r.SetManualStart(false)
				r.SetRefuseManualStart(false)
				r.SetRefuseManualStop(false)
			})
		}
	}()

	for i := 0; i < 30; i++ {
		WritePacket(conn, CmdStartService, append(EncodeHandle(handle), 0))
		readReply(t, conn)
		WritePacket(conn, CmdStopService, append(EncodeHandle(handle), 0))
		readReply(t, conn)
		WritePacket(conn, CmdStartAll, nil)
		readReply(t, conn)
	}
	close(stop)
	<-done
}
