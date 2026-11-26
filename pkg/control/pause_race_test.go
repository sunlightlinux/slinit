package control

import (
	"encoding/binary"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// pause, continue and signal read the service's pid (and pause/continue
// its paused flag) while the event loop restarts the process. They used to
// do it without queueMu, which -race reports as a DATA RACE against the
// start and exit paths; this test exists to give the detector that
// interleaving.
func TestPauseContinueSignalRaceRestart(t *testing.T) {
	server, sockPath := setupTestServer(t)
	defer server.Stop()

	ss := server.services
	svc := service.NewProcessService(ss, "race-pause")
	svc.SetCommand([]string{"/bin/sleep", "30"})
	svc.SetStopTimeout(2 * time.Second)
	ss.AddService(svc)
	defer ss.ForceStopService(svc)

	conn := connectTest(t, sockPath)
	defer conn.Close()
	handle := loadHandle(t, conn, "race-pause")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			ss.StartService(svc)
			time.Sleep(5 * time.Millisecond)
			ss.StopService(svc)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	sigPayload := make([]byte, 8)
	binary.LittleEndian.PutUint32(sigPayload, handle)
	binary.LittleEndian.PutUint32(sigPayload[4:], uint32(syscall.SIGCONT))
	for {
		select {
		case <-done:
			return
		default:
		}
		for _, req := range []struct {
			cmd     uint8
			payload []byte
		}{
			{CmdPauseService, EncodeHandle(handle)},
			{CmdContinueService, EncodeHandle(handle)},
			{CmdSignal, sigPayload},
		} {
			if err := WritePacket(conn, req.cmd, req.payload); err != nil {
				t.Fatal(err)
			}
			readReply(t, conn)
		}
	}
}
