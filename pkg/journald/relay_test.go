package journald

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

func credOOB(pid, uid int) []byte {
	return unix.UnixCredentials(&unix.Ucred{Pid: int32(pid), Uid: uint32(uid), Gid: uint32(uid)})
}

// PID 1 relays syslog and kernel messages with the identity it took from
// the kernel: the syslog sender's, or none for the kernel. The receiver
// used to stamp the relaying socket's credentials over it, so on disk a
// message from logger read _pid=1, _uid=0, _comm=slinit.
func TestRelayedIdentityKept(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport journal.Transport
		pid, uid  int
	}{
		{"syslog sender", journal.TransportSyslog, 4243, 1000},
		{"kernel message", journal.TransportKernel, 0, 0},
	} {
		evt := &journal.Event{Transport: tc.transport, Pid: tc.pid, Uid: tc.uid, Gid: tc.uid, Comm: "logger"}
		if tc.transport == journal.TransportKernel {
			evt.Comm = ""
		}
		stampFromSCM(evt, credOOB(1, 0)) // relayed by root (PID 1)
		if evt.Pid != tc.pid || evt.Uid != tc.uid || evt.Transport != tc.transport {
			t.Errorf("%s relayed by PID 1: pid %d uid %d transport %q; want the original %d %d %q",
				tc.name, evt.Pid, evt.Uid, evt.Transport, tc.pid, tc.uid, tc.transport)
		}
	}
}

// A client that is neither root nor the daemon's own user is stamped
// from the kernel as before, and cannot claim a relayed transport either.
func TestUnprivilegedClientCannotClaimRelay(t *testing.T) {
	uid := os.Getuid() + 1 // anyone but us
	if uid == 0 {
		uid = 1
	}
	evt := &journal.Event{Transport: journal.TransportSyslog, Pid: 1, Uid: 0, Comm: "sudo"}
	stampFromSCM(evt, credOOB(4300, uid))
	if evt.Pid != 4300 || evt.Uid != uid {
		t.Errorf("unprivileged client kept its claimed identity: pid %d uid %d", evt.Pid, evt.Uid)
	}
	if evt.Transport != journal.TransportNative {
		t.Errorf("unprivileged client's transport = %q, want native: it may not pose as a relay", evt.Transport)
	}
}
