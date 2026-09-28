package service

import (
	"fmt"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// freePort asks the kernel for a port, then releases it. Racy in principle,
// fine in a test: nothing else here is binding.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probing for a free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// reusePortSvc builds a service listening on 127.0.0.1:port, with
// SO_REUSEPORT on or off.
func reusePortSvc(t *testing.T, name string, port int, reuse bool) *ProcessService {
	t.Helper()
	set, _ := newTestSet()
	svc := NewProcessService(set, name)
	svc.SetCommand([]string{"/bin/sleep", "60"})
	svc.Record().SetSocketPaths([]string{fmt.Sprintf("tcp:127.0.0.1:%d", port)})
	svc.Record().SetSocketReusePort(reuse)
	set.AddService(svc)
	return svc
}

// Without SO_REUSEPORT a second service cannot take the same port. This is
// the baseline the feature exists to lift: it documents that the second
// bind really does fail, so the test below is not passing for some
// unrelated reason.
func TestSocketSecondBindFailsWithoutReusePort(t *testing.T) {
	port := freePort(t)

	first := reusePortSvc(t, "first", port, false)
	if err := first.openSocket(); err != nil {
		t.Fatalf("first bind should succeed: %v", err)
	}
	defer first.closeSocket()

	second := reusePortSvc(t, "second", port, false)
	if err := second.openSocket(); err == nil {
		second.closeSocket()
		t.Fatal("second bind on the same port succeeded without SO_REUSEPORT")
	}
}

// With it, both bind, which is what lets N template instances share a hot
// port and have the kernel spread connections between them.
func TestSocketReusePortAllowsSharedPort(t *testing.T) {
	port := freePort(t)

	first := reusePortSvc(t, "web@1", port, true)
	if err := first.openSocket(); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	defer first.closeSocket()

	second := reusePortSvc(t, "web@2", port, true)
	if err := second.openSocket(); err != nil {
		t.Fatalf("second bind with SO_REUSEPORT should succeed: %v", err)
	}
	defer second.closeSocket()

	third := reusePortSvc(t, "web@3", port, true)
	if err := third.openSocket(); err != nil {
		t.Fatalf("third bind with SO_REUSEPORT should succeed: %v", err)
	}
	third.closeSocket()
}

// Assert the option on the descriptor slinit actually hands the child, not
// just that two binds happened to work — a kernel that allowed the second
// bind for some other reason would otherwise look like success.
func TestSocketReusePortIsSetOnThePassedFD(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reuse bool
		want  int
	}{
		{"on", true, 1},
		{"off", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := reusePortSvc(t, "fdcheck-"+tc.name, freePort(t), tc.reuse)
			if err := svc.openSocket(); err != nil {
				t.Fatalf("openSocket: %v", err)
			}
			defer svc.closeSocket()

			got, err := unix.GetsockoptInt(int(svc.socketFD.Fd()),
				unix.SOL_SOCKET, unix.SO_REUSEPORT)
			if err != nil {
				t.Fatalf("getsockopt SO_REUSEPORT: %v", err)
			}
			if (got != 0) != (tc.want != 0) {
				t.Errorf("SO_REUSEPORT on the passed fd = %d, want %d", got, tc.want)
			}
		})
	}
}

// A Unix socket is unaffected: the kernel accepts the option and does
// nothing with it, so slinit does not set it there rather than imply a
// guarantee it is not making. Setting the directive must still not break
// such a service.
func TestSocketReusePortIgnoredForUnixSocket(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "unix-reuse")
	svc.SetCommand([]string{"/bin/sleep", "60"})
	sockPath := t.TempDir() + "/s.sock"
	// -1/-1 for uid/gid, which is what NewServiceDescription defaults to;
	// constructing the record directly would otherwise chown to root and
	// fail as a non-root test process.
	svc.Record().SetSocketDetails(sockPath, 0660, -1, -1)
	svc.Record().SetSocketReusePort(true)
	set.AddService(svc)

	if err := svc.openSocket(); err != nil {
		t.Fatalf("a Unix socket with socket-reuseport set should still open: %v", err)
	}
	defer svc.closeSocket()
}
