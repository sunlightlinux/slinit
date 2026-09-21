// Regression tests derived from the systemd bug list on nosystemd.org.
// See pkg/journal/nosystemd_checklist_test.go for the rationale.
package journald

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/journal"
)

// waitForEvents polls the sink until it holds at least n events or the
// deadline passes. The receiver is asynchronous; a local Unix socket
// settles in single-digit milliseconds, so anything past 2s is real
// breakage rather than slowness.
func waitForEvents(sink *captureSink, n int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.len() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// systemd#2913: "journald is unable to attribute messages incoming from
// processes that exited to their cgroup, due to /proc vs SCM_CREDS
// race". journald read the sender's cgroup out of /proc *after* taking
// the datagram, so a process that logged and immediately exited had its
// message filed against no unit at all — exactly the messages you most
// want attributed, since a dying process is usually why you are reading
// the log.
// https://github.com/systemd/systemd/issues/2913
//
// slinit does not attribute by /proc. Identity (Pid/Uid/Gid) comes from
// SCM_CREDENTIALS, which the kernel stamps at send time and which stays
// correct however fast the sender exits. Unit attribution comes from the
// event itself — PID 1's log pipeline knows which service it captured
// output from, so there is no lookup to lose. Only the cosmetic
// Comm/Exe/Cmdline fields are read from /proc, and they degrade to
// empty rather than to something wrong.
//
// This test sends from a process that has exited by the time the daemon
// gets around to the datagram, and pins that the parts that matter
// survive.
func TestExitedSenderKeepsAttribution_systemd2913(t *testing.T) {
	journal.InitIDs("testhost")

	sink := &captureSink{}
	recv, err := NewReceiver(tmpSocketPath(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv.Run(ctx)
	defer recv.Stop() //nolint: errcheck

	// A child that emits one event and exits immediately. By the time
	// the receiver handles the datagram the sender is gone, which is
	// precisely the upstream race.
	helper := exec.Command(os.Args[0], "-test.run=TestJournaldEmitHelperProcess")
	helper.Env = append(os.Environ(),
		"SLINIT_JOURNALD_HELPER=1",
		"SLINIT_JOURNALD_SOCKET="+recv.Path(),
	)
	if out, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	// The helper has exited — its /proc entry is gone from here on.
	helperPid := helper.ProcessState.Pid()

	waitForEvents(sink, 1)
	if sink.len() != 1 {
		t.Fatalf("received %d events, want 1 — a message from an exited "+
			"sender was dropped entirely", sink.len())
	}
	evt := sink.events[0]

	// The payload must survive. This is the whole point.
	if evt.Msg != "from-a-dying-process" {
		t.Errorf("message body lost or altered: %q", evt.Msg)
	}
	if evt.Unit != "doomed-svc" {
		t.Errorf("unit attribution lost: %q — systemd#2913 is exactly this "+
			"field going missing when the sender exits", evt.Unit)
	}

	// Identity comes from SCM_CREDENTIALS, so it must be the child's
	// real pid even though /proc/<pid> no longer exists.
	if evt.Pid != helperPid {
		t.Errorf("_pid = %d, want the exited child's %d — identity must come "+
			"from SCM_CREDENTIALS, not from a /proc lookup", evt.Pid, helperPid)
	}
	if evt.Uid != os.Getuid() {
		t.Errorf("_uid = %d, want %d", evt.Uid, os.Getuid())
	}

	// Comm/Exe/Cmdline are the fields that legitimately cannot be
	// recovered. They must be empty rather than carrying whatever the
	// client claimed — trusting the client here would turn a missing
	// field into a forgeable one.
	if evt.Comm == "i-am-root" || evt.Exe == "/usr/bin/totally-legit" {
		t.Errorf("client-supplied _comm/_exe were trusted for an exited "+
			"sender: comm=%q exe=%q", evt.Comm, evt.Exe)
	}
}

// The other half of the same invariant: for a sender that is still
// alive, the trusted fields must be the /proc truth and not the
// client's claim. Without this, "clear the fields first" could be
// satisfied by never populating them at all.
func TestLiveSenderClaimIsOverwritten_systemd2913(t *testing.T) {
	journal.InitIDs("testhost")

	sink := &captureSink{}
	recv, err := NewReceiver(tmpSocketPath(t), sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	recv.Run(ctx)
	defer recv.Stop() //nolint: errcheck

	buf := journal.NewEventBuffer(4)
	e := journal.NewEmitter(buf, recv.Path())
	defer e.Close()

	if err := e.Emit(&journal.Event{
		Msg:  "still-running",
		Unit: "live-svc",
		Prio: journal.PriorityInfo,
		Comm: "i-am-root",
		Exe:  "/usr/bin/totally-legit",
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	waitForEvents(sink, 1)
	if sink.len() != 1 {
		t.Fatalf("received %d events, want 1", sink.len())
	}
	evt := sink.events[0]

	if evt.Comm == "i-am-root" {
		t.Error("a live sender's claimed _comm was trusted")
	}
	if evt.Exe == "/usr/bin/totally-legit" {
		t.Error("a live sender's claimed _exe was trusted")
	}
	// It must actually be filled in from /proc, not merely blanked.
	if evt.Comm == "" {
		t.Error("_comm was cleared but never populated for a live sender")
	}
}

// TestJournaldEmitHelperProcess is not a test. It is the child half of
// the case above, selected by -test.run and gated on an env var so a
// normal `go test` run skips it.
func TestJournaldEmitHelperProcess(t *testing.T) {
	if os.Getenv("SLINIT_JOURNALD_HELPER") != "1" {
		t.Skip("helper process; driven by TestExitedSenderKeepsAttribution_systemd2913")
	}
	journal.InitIDs("testhost")

	buf := journal.NewEventBuffer(4)
	e := journal.NewEmitter(buf, os.Getenv("SLINIT_JOURNALD_SOCKET"))
	defer e.Close()

	// Claim metadata the daemon must not believe.
	_ = e.Emit(&journal.Event{
		Msg:     "from-a-dying-process",
		Unit:    "doomed-svc",
		Prio:    journal.PriorityError,
		Comm:    "i-am-root",
		Exe:     "/usr/bin/totally-legit",
		Cmdline: "totally-legit --trust-me",
	})
	// Return immediately; the process exits while the datagram is
	// still in the socket buffer.
}
