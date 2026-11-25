package control

import (
	"testing"
	"time"

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
		{"superfast", []byte{uint8(service.ShutdownPoweroff), ShutdownFlagSuper}, ShutdownFlagSuper},
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

// A shutdown type outside remain..kexec is refused, not ACKed: PID 1's
// shutdown path takes anything it does not know as a halt, so a raw
// client's None or stray byte held at a boot-failure menu halted the
// machine. Every valid type, kexec and softreboot included, still goes
// through, and a scheduled shutdown is checked the same way.
func TestShutdownTypeValidated(t *testing.T) {
	for _, b := range []uint8{uint8(service.ShutdownNone), uint8(service.ShutdownKexec) + 1, 0xff} {
		server, sockPath := setupTestServer(t)
		called := false
		server.ShutdownFunc = func(service.ShutdownType, uint8) { called = true }
		conn := connectTest(t, sockPath)
		if err := WritePacket(conn, CmdShutdown, []byte{b}); err != nil {
			t.Fatalf("write: %v", err)
		}
		if rply, _ := readReply(t, conn); rply != RplyBadReq {
			t.Errorf("type %d: reply = %d, want BadReq", b, rply)
		}
		conn.Close()
		server.Stop()
		if called {
			t.Errorf("type %d reached ShutdownFunc", b)
		}
		if _, _, _, ok := DecodeScheduleShutdown([]byte{b, 0, 0, 0, 60}); ok {
			t.Errorf("scheduled shutdown of type %d decoded as valid", b)
		}
	}
	for st := service.ShutdownRemain; st <= service.ShutdownKexec; st++ {
		server, sockPath := setupTestServer(t)
		got := make(chan service.ShutdownType, 1)
		server.ShutdownFunc = func(s service.ShutdownType, _ uint8) { got <- s }
		conn := connectTest(t, sockPath)
		if err := WritePacket(conn, CmdShutdown, []byte{uint8(st)}); err != nil {
			t.Fatalf("write: %v", err)
		}
		if rply, _ := readReply(t, conn); rply != RplyACK {
			t.Errorf("%v: reply = %d, want ACK", st, rply)
		}
		if s := <-got; s != st {
			t.Errorf("%v delivered as %v", st, s)
		}
		conn.Close()
		server.Stop()
		if _, _, _, ok := DecodeScheduleShutdown(EncodeScheduleShutdown(st, time.Minute, "")); !ok {
			t.Errorf("scheduled %v refused", st)
		}
	}
}
