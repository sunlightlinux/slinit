package control

import (
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// CmdServiceRusage reads the service's pid without queueMu. A scripted
// service's PID() used to read startPID/stopPID bare while its start and
// stop paths wrote them under the lock; -race reports it.
func TestServiceRusageScriptedPIDRace(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	ss := server.services
	svc := service.NewScriptedService(ss, "race-scripted")
	svc.SetStartCommand([]string{"/bin/sleep", "0.01"})
	svc.SetStopCommand([]string{"/bin/sleep", "0.01"})
	ss.AddService(svc)
	defer ss.StopService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := loadHandle(t, conn, "race-scripted")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			ss.StartService(svc)
			time.Sleep(20 * time.Millisecond)
			ss.StopService(svc)
			time.Sleep(20 * time.Millisecond)
		}
	}()

	payload := append([]byte{RusageScopeService}, EncodeHandle(handle)...)
	for {
		select {
		case <-done:
			return
		default:
		}
		if err := WritePacket(conn, CmdServiceRusage, payload); err != nil {
			t.Fatal(err)
		}
		readReply(t, conn)
	}
}
