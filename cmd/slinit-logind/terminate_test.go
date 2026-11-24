package main

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// testScope never exists, so nothing here can reach a real session's
// cgroup.kill even when the tests run as root on a host running
// slinit-logind; who is "in" it comes from scopeProcs.
const testScope = "/sys/fs/cgroup/slinit-logind-test-scope"

// inScope makes scopeProcs report the given processes for testScope.
func inScope(t *testing.T, pids ...int) {
	t.Helper()
	orig := scopeProcs
	scopeProcs = func(scope string) []int {
		if scope == testScope {
			return pids
		}
		return nil
	}
	t.Cleanup(func() { scopeProcs = orig })
}

// startInSession starts cmd and returns a channel closed once it has
// been reaped.
func startInSession(t *testing.T, cmd *exec.Cmd) chan struct{} {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return done
}

// Terminate used to drop the session's records and nothing else: every
// process of the session kept running, no longer tracked by anything.
// systemd stops the session's scope, which kills them. Each door —
// the three Manager calls and the user and seat objects' Terminate —
// must end the session's processes, not only its bookkeeping.
func TestTerminateKillsTheSessionsProcesses(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(m *manager) *dbus.Error
	}{
		{"TerminateSession", func(m *manager) *dbus.Error { return m.TerminateSession(root, "c1") }},
		{"TerminateUser", func(m *manager) *dbus.Error { return m.TerminateUser(root, 1000) }},
		{"TerminateSeat", func(m *manager) *dbus.Error { return m.TerminateSeat(root, "seat0") }},
		{"User.Terminate", func(m *manager) *dbus.Error { return (&userObject{m: m, uid: 1000}).Terminate(root) }},
		{"Seat.Terminate", func(m *manager) *dbus.Error { return (&seatObject{m: m, id: "seat0"}).Terminate(root) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := usersBus(t)
			cmd := exec.Command("sleep", "30")
			done := startInSession(t, cmd)
			inScope(t, cmd.Process.Pid)
			if err := writeJSONAtomic(sessionFile("c1"), SessionRecord{ID: "c1", UserID: 1000, SeatID: "seat0",
				LeaderPID: uint32(cmd.Process.Pid), Scope: testScope}); err != nil {
				t.Fatal(err)
			}

			if err := tc.call(m); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s returned; session record gone=%v, but its process %d still runs",
					tc.name, !sessionExistsLocked("c1"), cmd.Process.Pid)
			}
			if sessionExistsLocked("c1") {
				t.Error("session record still present")
			}
		})
	}
}

// A process that ignores SIGTERM and SIGHUP does not outlive the session:
// what is left after the grace period is SIGKILLed, as systemd's scope
// stop does after its timeout.
func TestTerminateSessionKillsSurvivors(t *testing.T) {
	m := usersBus(t)
	orig := terminateGrace
	terminateGrace = 100 * time.Millisecond
	t.Cleanup(func() { terminateGrace = orig })

	// SIG_IGN survives exec, so the sleep ignores both.
	cmd := exec.Command("sh", "-c", `trap "" TERM HUP; exec sleep 30`)
	done := startInSession(t, cmd)
	pid := cmd.Process.Pid
	waitUntil(t, 2*time.Second, "the helper to exec sleep", func() bool {
		b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
		return strings.TrimSpace(string(b)) == "sleep"
	})
	inScope(t, pid)
	if err := writeJSONAtomic(sessionFile("c1"), SessionRecord{ID: "c1", UserID: 1000, SeatID: "seat0",
		LeaderPID: uint32(pid), Scope: testScope}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := m.TerminateSession(root, "c1"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > terminateGrace {
		t.Errorf("TerminateSession blocked the caller for %s", took)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("process %d ignoring SIGTERM/SIGHUP survived the session", pid)
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Errorf("survivor ended with %v, want SIGKILL", cmd.ProcessState)
	}
}
