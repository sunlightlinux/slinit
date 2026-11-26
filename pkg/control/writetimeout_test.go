package control

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// A write that timed out on a slow reader only marked the connection
// closed; the socket stayed open and serve() kept treating read
// timeouts as idleness. A client that resumed reading got what was
// buffered, possibly a torn packet, and then waited for ever. It must
// see EOF instead.
func TestWriteTimeoutClosesConnection(t *testing.T) {
	saved := writeDeadline
	writeDeadline = 50 * time.Millisecond
	defer func() { writeDeadline = saved }()

	journal.InitIDs("testhost")
	buf := journal.NewEventBuffer(32)
	e := journal.NewEmitter(buf, "/nonexistent/slinit-test-journal.sock")
	journal.SetGlobal(e)
	t.Cleanup(func() { journal.SetGlobal(nil) })

	server, sockPath := setupTestServer(t)
	defer server.Stop()
	conn := connectTest(t, sockPath)
	defer conn.Close()
	if err := WritePacket(conn, CmdJournalSubscribe, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	// Fill the socket buffer without reading, past the write deadline.
	msg := strings.Repeat("x", 4000)
	for i := 0; i < 3000; i++ {
		_ = e.Emit(&journal.Event{Msg: msg, Unit: "cron"})
		if i%100 == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(300 * time.Millisecond)

	for n := 0; ; n++ {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := ReadPacket(conn); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatalf("after %d packets the server went silent with the socket open", n)
			}
			return // EOF or a torn packet: the client is told
		}
	}
}
