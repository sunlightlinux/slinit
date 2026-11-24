package journald

import (
	"os"
	"strings"
	"testing"
	"time"

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

// events.sock is 0666, so any local user can send to it. Before the fix
// the receiver stamped only pid/uid/gid/comm/exe/cmdline from the
// kernel: a forged _boot_id/_machine_id/_hostname, ts/mts, unit and a
// client field such as _PID went to disk as sent, and the binary reader
// let the field override the kernel-attested _PID. A forged mts of
// MaxInt64 also landed in the persisted marker and hid the shutdown tail.
func TestUntrustedClientCannotForgeTrustedFields(t *testing.T) {
	journal.InitIDs("testhost")
	uid := os.Getuid() + 1
	if uid == 0 {
		uid = 1
	}
	evt := &journal.Event{
		Ts: 1, Mts: 1<<63 - 1, Msg: "forged", Prio: journal.PriorityInfo,
		Unit:   "sshd",
		BootID: "00000000000000000000000000000000", MachineID: "11111111111111111111111111111111",
		Hostname: "elsewhere", Exe: "/usr/sbin/sshd",
		Fields: map[string]string{"_PID": "1", "_TRANSPORT": "driver", "_COMM": "slinit", "NOTE": "ok"},
	}
	before := time.Now().UnixNano()
	stampFromSCM(evt, credOOB(4300, uid))

	if evt.BootID != journal.BootID() || evt.MachineID != journal.MachineID() || evt.Hostname != journal.Hostname() {
		t.Errorf("ids kept: boot %q machine %q host %q", evt.BootID, evt.MachineID, evt.Hostname)
	}
	if evt.Ts < before || evt.Mts == 1<<63-1 {
		t.Errorf("timestamps kept: ts %d mts %d", evt.Ts, evt.Mts)
	}
	if evt.Unit != "" {
		t.Errorf("unit %q kept: an unprivileged sender may not claim a service", evt.Unit)
	}
	for k := range evt.Fields {
		if strings.HasPrefix(k, "_") {
			t.Errorf("client field %s kept", k)
		}
	}
	if evt.Fields["NOTE"] != "ok" {
		t.Errorf("ordinary client field lost: %v", evt.Fields)
	}
}

// events.sock had no rate limit: one local user could write the journal
// as fast as the daemon stored it (222 MiB/s measured), and vacuum then
// deleted everyone's history to make room. Unprivileged senders now get
// /dev/log's per-UID limit; root (PID 1's relay) is never limited.
func TestReceiverRateLimitsUnprivilegedSenders(t *testing.T) {
	journal.InitIDs("testhost")
	sink := &captureSink{}
	r := &Receiver{sink: sink, limit: journal.NewUIDLimiter()}
	uid := os.Getuid() + 1
	if uid == 0 {
		uid = 1
	}
	msg := []byte(`{"ts":1,"msg":"flood","prio":6}`)
	for i := 0; i < 1000; i++ {
		r.handle(msg, credOOB(4300, uid))
	}
	if n := sink.len(); n != journal.UIDRateLimit(uint32(uid)) {
		t.Errorf("uid %d stored %d of 1000 messages in one window", uid, n)
	}
	before := sink.len()
	for i := 0; i < 1500; i++ {
		r.handle(msg, credOOB(1, 0))
	}
	if got := sink.len() - before; got != 1500 {
		t.Errorf("root relay stored %d of 1500 messages: PID 1 must not be limited", got)
	}
}
