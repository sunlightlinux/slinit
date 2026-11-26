package control

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A start (or start --pin) that passed the shutting-down check just
// before a shutdown began used to start, and pin, the service after
// StopAllServices had stopped and unpinned everything. Nothing stops it
// again, so the shutdown waited out its full timeout (90s) for a
// service that was explicitly active. A -race stress run of the daemon
// with concurrent "slinitctl --no-wait [--pin] start" loops hung its
// final shutdown that way in every run. The check and the start must be
// one step under queueMu, as they are in dinit's single-threaded loop.
func TestStartRacingShutdownLeavesNothingStarted(t *testing.T) {
	for _, startAll := range []bool{false, true} {
		t.Run(fmt.Sprintf("start-all=%v", startAll), func(t *testing.T) {
			for attempt := 0; attempt < 100; attempt++ {
				if leftover := startRacingShutdown(t, startAll); leftover != "" {
					t.Fatalf("attempt %d: %s still STARTED after the shutdown stopped everything", attempt, leftover)
				}
			}
		})
	}
}

func startRacingShutdown(t *testing.T, startAll bool) string {
	server, sockPath := setupTestServer(t)
	defer server.Stop()
	ss := server.services

	const n = 8
	svcs := make([]service.Service, n)
	handles := make([]uint32, n)
	conns := make([]interface{ Close() error }, 0, n)
	for i := range svcs {
		svcs[i] = service.NewInternalService(ss, fmt.Sprintf("sr-%d", i))
		ss.AddService(svcs[i])
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	netConns := make([]net.Conn, n)
	for i := 0; i < n; i++ {
		netConns[i] = connectTest(t, sockPath)
		conns = append(conns, netConns[i])
		handles[i] = findHandle(t, netConns[i], svcs[i].Name())
	}
	// Workers start only once every handle is found: a start-all from one
	// of them sends service events to the others' connections.
	for i := 0; i < n; i++ {
		conn := netConns[i]
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var flags uint8
				if i%2 == 0 {
					flags = 0x01 // --pin
				}
				cmd, payload := CmdStartService, append(EncodeHandle(handles[i]), flags)
				if startAll {
					cmd, payload = CmdStartAll, nil
				}
				if WritePacket(conn, cmd, payload) != nil {
					return
				}
				if _, _, err := ReadPacket(conn); err != nil {
					return
				}
			}
		}(i)
	}

	time.Sleep(2 * time.Millisecond)
	ss.StopAllServices(service.ShutdownPoweroff)
	time.Sleep(5 * time.Millisecond)
	close(stop)
	wg.Wait()
	for _, c := range conns {
		c.Close()
	}

	deadline := time.Now().Add(time.Second)
	for {
		leftover := ""
		for _, s := range svcs {
			if s.State() != service.StateStopped {
				leftover = s.Name()
			}
		}
		if leftover == "" || time.Now().After(deadline) {
			return leftover
		}
		time.Sleep(5 * time.Millisecond)
	}
}
