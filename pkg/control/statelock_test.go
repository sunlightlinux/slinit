package control

import (
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// churnServices starts and stops the given services under the queue
// lock, as the event loop and other clients do, until stop is closed.
func churnServices(set *service.ServiceSet, stop <-chan struct{}, svcs ...service.Service) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, s := range svcs {
				set.StartService(s)
			}
			for i := len(svcs) - 1; i >= 0; i-- {
				set.StopService(svcs[i])
			}
		}
	}()
	return &wg
}

// sendFor repeats send for d.
func sendFor(t *testing.T, d time.Duration, send func() error) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
}

// A trigger set over the socket can start the service and, through
// Started, its dependents and the queues; it must take the queue lock
// like every other state change. Run under -race: before the fix the
// detector reported SetTrigger against the churning goroutine.
func TestSetTriggerHoldsQueueLock(t *testing.T) {
	server, sock := setupTestServer(t)
	defer server.Stop()
	set := server.services
	trig := service.NewTriggeredService(set, "t")
	set.AddService(trig)
	dept := service.NewInternalService(set, "d")
	set.AddService(dept)
	dept.AddDep(trig, service.DepRegular)
	other := service.NewInternalService(set, "x")
	set.AddService(other)

	conn := connectTest(t, sock)
	defer conn.Close()
	h := findHandle(t, conn, "t")

	stop := make(chan struct{})
	wg := churnServices(set, stop, dept, other)
	sendFor(t, 500*time.Millisecond, func() error {
		for _, v := range []byte{1, 0} {
			if err := WritePacket(conn, CmdSetTrigger, append(EncodeHandle(h), v)); err != nil {
				return err
			}
			readReply(t, conn)
		}
		return nil
	})
	close(stop)
	wg.Wait()
}

// Release, stop --pin, once and reset-failed change service state from
// a connection goroutine; each must do so under the queue lock.
func TestServiceCommandsHoldQueueLock(t *testing.T) {
	server, sock := setupTestServer(t)
	defer server.Stop()
	set := server.services
	a := service.NewInternalService(set, "a")
	set.AddService(a)
	other := service.NewInternalService(set, "x")
	set.AddService(other)

	conn := connectTest(t, sock)
	defer conn.Close()
	h := findHandle(t, conn, "a")

	stop := make(chan struct{})
	wg := churnServices(set, stop, a, other)
	sendFor(t, 500*time.Millisecond, func() error {
		cmds := []struct {
			cmd     uint8
			payload []byte
		}{
			{CmdReleaseService, EncodeHandle(h)},
			{CmdStopService, append(EncodeHandle(h), 0x01|StopFlagRestarting)},
			{CmdUnpinService, EncodeHandle(h)},
			{CmdOnceService, EncodeHandle(h)},
			{CmdResetFailed, EncodeHandle(h)},
		}
		for _, c := range cmds {
			if err := WritePacket(conn, c.cmd, c.payload); err != nil {
				return err
			}
			readReply(t, conn)
		}
		return nil
	})
	close(stop)
	wg.Wait()
	set.UnpinService(a)
}
