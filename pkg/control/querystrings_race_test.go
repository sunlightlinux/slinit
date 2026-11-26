package control

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// The description, metadata and bundle-members queries read string and
// slice fields that a reload rewrites under the graph lock
// (config.applyToService and the loader's update path run inside
// WithGraphLock). They read them without the lock, so a reply could be
// built from a torn string or slice header; -race reported the
// description read in a stress run of the daemon. This test gives the
// detector that interleaving for all three.
func TestQueryStringsUnderReload(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	ss := server.services
	target := service.NewInternalService(ss, "qs-target")
	ss.AddService(target)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := findHandle(t, conn, "qs-target")

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
				r.SetDescription("d")
				r.SetAuthor("a")
				r.SetVersion("v")
				r.SetUsage("u")
				r.SetBundleMembers([]string{"m"})
			})
		}
	}()

	for i := 0; i < 30; i++ {
		for _, cmd := range []uint8{CmdQueryDescription, CmdQueryMetadata, CmdQueryBundleMembers} {
			WritePacket(conn, cmd, EncodeHandle(handle))
			readReply(t, conn)
		}
	}
	close(stop)
	<-done
}
