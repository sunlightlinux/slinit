package control

import (
	"fmt"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// Unload checked STOPPED, HasLoneRef and "no other client holds a handle"
// and only then removed the service, none of it under one lock. A find
// from another client landing in between got a handle to the service just
// unloaded: one it could start outside the set, where nothing could find
// or stop it. The checks and the removal are now one step under the graph
// lock, and a handle allocated for a service already gone is refused.
func TestUnloadRacingFindGivesNoHandleToUnloaded(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services

	a := connectTest(t, sockPath)
	defer a.Close()
	b := connectTest(t, sockPath)
	defer b.Close()

	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("u%d", i)
		svc := service.NewInternalService(ss, name)
		ss.AddService(svc)
		ha := findHandle(t, a, name)

		bReply := make(chan uint8, 1)
		go func() {
			if err := WritePacket(b, CmdFindService, EncodeServiceName(name)); err != nil {
				bReply <- 0
				return
			}
			r, _, err := ReadPacket(b)
			if err != nil {
				r = 0
			}
			bReply <- r
		}()
		if err := WritePacket(a, CmdUnloadService, EncodeHandle(ha)); err != nil {
			t.Fatal(err)
		}
		ra, _ := readReply(t, a)
		rb := <-bReply

		if ra == RplyACK && rb == RplyServiceRecord {
			t.Fatalf("iteration %d: unload ACKed and the racing find got a handle to the unloaded %s (in set: %v)",
				i, name, ss.FindService(name, false) != nil)
		}
		if ra != RplyACK {
			// The find won: b's handle keeps it loaded. Let it go.
			ss.UnloadService(svc)
		}
	}
}
