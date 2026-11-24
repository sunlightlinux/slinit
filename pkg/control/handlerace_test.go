package control

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A connection's handle maps are written by its own goroutine
// (allocHandle / handleCloseHandle) and read by whichever goroutine
// drives the state machine (ServiceEvent -> findHandle): a process
// exit, a timer, another connection. Unlocked, that is Go's
// unrecoverable "concurrent map read and map write" — in PID 1, a
// kernel panic. slinit-monitor with two names, add-dep and enable all
// take a second handle while the first service may be changing state.
func TestHandleMapsSafeAgainstServiceEvents(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	flip := service.NewInternalService(server.services, "flip")
	server.services.AddService(flip)
	for i := 0; i < 64; i++ {
		server.services.AddService(service.NewInternalService(server.services, fmt.Sprintf("s%d", i)))
	}

	conn := connectTest(t, sockPath)
	defer conn.Close()
	// Drain events in background so writes never block.
	findHandle(t, conn, "flip") // subscribe to flip's events

	var stop atomic.Bool
	done := make(chan struct{})
	go func() { // state machine side: flip generates events
		defer close(done)
		for !stop.Load() {
			server.services.StartService(flip)
			server.services.StopService(flip)
		}
	}()

	for round := 0; round < 200; round++ {
		var hs []uint32
		for i := 0; i < 64; i++ {
			WritePacket(conn, CmdFindService, EncodeServiceName(fmt.Sprintf("s%d", i)))
			_, p := readReply(t, conn)
			h, _ := DecodeHandle(p[1:])
			hs = append(hs, h)
		}
		for _, h := range hs {
			WritePacket(conn, CmdCloseHandle, EncodeHandle(h))
			readReply(t, conn)
		}
	}
	stop.Store(true)
	<-done
}
