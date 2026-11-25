package control

import (
	"os"
	"testing"
	"time"
)

// Stop waits for its connection goroutines, and exitFlushed gives it two
// seconds. A goroutine stuck in a hook must not keep the socket file
// there past that: it has to be gone once the listener is closed, not
// only after every connection has finished.
func TestStopRemovesSocketWithStuckConnection(t *testing.T) {
	server, sockPath := setupTestServer(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	server.SuspendFunc = func(string) error {
		close(entered)
		<-release
		return nil
	}
	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdSuspend, nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	<-entered
	go server.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(sockPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket file still there 2s into Stop with a connection stuck in a hook")
}
