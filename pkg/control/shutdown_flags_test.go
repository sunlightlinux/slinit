package control

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// CmdShutdown carries an optional flags byte saying how much haste the
// operator is in. A payload without it — every pre-flags client, and
// the SysV shims — must still mean the graceful teardown, and a payload
// with it must deliver exactly what was asked: lose the byte quietly and
// `shutdown halt --fast` becomes an ordinary shutdown that waits every
// service out.
func TestShutdownFlagsReachTheDaemon(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    uint8
	}{
		{"no flags byte at all", []byte{uint8(service.ShutdownHalt)}, 0},
		{"explicit zero", []byte{uint8(service.ShutdownHalt), 0}, 0},
		{"kill", []byte{uint8(service.ShutdownHalt), ShutdownFlagKill}, ShutdownFlagKill},
		{"fast", []byte{uint8(service.ShutdownReboot), ShutdownFlagFast}, ShutdownFlagFast},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, sockPath := setupTestServer(t)
			defer server.Stop()

			gotType := service.ShutdownNone
			var gotFlags uint8
			done := make(chan struct{}, 1)
			server.ShutdownFunc = func(st service.ShutdownType, fl uint8) {
				gotType, gotFlags = st, fl
				done <- struct{}{}
			}

			conn := connectTest(t, sockPath)
			defer conn.Close()

			if err := WritePacket(conn, CmdShutdown, tc.payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			if rply, _ := readReply(t, conn); rply != RplyACK {
				t.Fatalf("reply = %d, want ACK", rply)
			}
			<-done

			if gotType != service.ShutdownType(tc.payload[0]) {
				t.Errorf("shutdown type = %v, want %v", gotType, service.ShutdownType(tc.payload[0]))
			}
			if gotFlags != tc.want {
				t.Errorf("flags = %#x, want %#x", gotFlags, tc.want)
			}
		})
	}
}
