package main

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestParseRetryInteger(t *testing.T) {
	steps, err := ParseRetry("5", syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("len=%d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 5*time.Second {
		t.Errorf("step 0: %+v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1: %+v", steps[1])
	}
}

func TestParseRetrySlashSpec(t *testing.T) {
	steps, err := ParseRetry("TERM/30/KILL/5", syscall.SIGTERM)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("len=%d", len(steps))
	}
	if steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 30*time.Second {
		t.Errorf("step 0: %+v", steps[0])
	}
	if steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("step 1: %+v", steps[1])
	}
}

func TestParseSignalForms(t *testing.T) {
	cases := map[string]syscall.Signal{
		"TERM":    syscall.SIGTERM,
		"SIGTERM": syscall.SIGTERM,
		"15":      syscall.SIGTERM,
		"HUP":     syscall.SIGHUP,
		"USR1":    syscall.SIGUSR1,
	}
	for in, want := range cases {
		got, err := ParseSignal(in)
		if err != nil || got != want {
			t.Errorf("ParseSignal(%q) = (%v, %v), want %v", in, got, err, want)
		}
	}
	if _, err := ParseSignal("BOGUS"); err == nil {
		t.Errorf("expected error for BOGUS")
	}
}

// OpenRC and Debian document signals in a schedule with a leading '-'
// ("-TERM/30/-KILL/5") and "forever" to repeat what follows. Both were
// rejected, so the supervisor never stopped its daemon: stopDaemon
// returned the parse error without sending anything.
func TestParseRetryDashAndForever(t *testing.T) {
	steps, err := ParseRetry("-TERM/30/-KILL/5", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("-SIG form: %v", err)
	}
	if len(steps) != 2 || steps[0].Signal != syscall.SIGTERM || steps[0].Timeout != 30*time.Second ||
		steps[1].Signal != syscall.SIGKILL || steps[1].Timeout != 5*time.Second {
		t.Errorf("-TERM/30/-KILL/5 = %+v", steps)
	}
	if steps, err := ParseRetry("-9/5", syscall.SIGTERM); err != nil || steps[0].Signal != syscall.SIGKILL {
		t.Errorf("-9/5 = %+v, %v; want SIGKILL", steps, err)
	}

	steps, err = ParseRetry("TERM/5/forever/KILL/1", syscall.SIGTERM)
	if err != nil {
		t.Fatalf("forever: %v", err)
	}
	want := []Step{
		{Signal: syscall.SIGTERM, Timeout: 5 * time.Second},
		{Signal: syscall.SIGKILL, Timeout: time.Second},
		{Repeat: true, RepeatFrom: 1},
	}
	if len(steps) != len(want) {
		t.Fatalf("TERM/5/forever/KILL/1 = %+v", steps)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], want[i])
		}
	}
	if _, unbounded := scheduleBudget(steps); !unbounded {
		t.Errorf("a forever schedule has a bounded budget")
	}

	// Trailing forever: nothing to repeat, wait for the exit.
	steps, err = ParseRetry("TERM/5/forever", syscall.SIGTERM)
	if err != nil || len(steps) != 2 || steps[1].Signal != 0 || steps[1].Timeout != 0 {
		t.Errorf("TERM/5/forever = %+v, %v", steps, err)
	}
	if _, err := ParseRetry("forever/TERM/1/forever", syscall.SIGTERM); err == nil {
		t.Errorf("two forevers accepted")
	}
	// A signal followed by a signal is sent without waiting.
	steps, err = ParseRetry("HUP/TERM/5", syscall.SIGTERM)
	if err != nil || len(steps) != 2 || steps[0].Timeout != noWait {
		t.Errorf("HUP/TERM/5 = %+v, %v", steps, err)
	}
}

// stopDaemon runs a "forever" schedule against a daemon that ignores
// SIGTERM: the repeated KILL ends it.
func TestStopDaemonForeverSchedule(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	opts := Options{
		Service: "test",
		Exec:    sh,
		Args:    []string{"-c", "trap '' TERM; exec sleep 60"},
		Retry:   "-TERM/1/forever/-KILL/1",
	}
	pid, exitCh, err := startDaemon(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	time.Sleep(100 * time.Millisecond)
	if err := stopDaemon(opts, pid); err != nil {
		t.Errorf("stopDaemon: %v", err)
	}
	select {
	case <-exitCh:
	case <-time.After(5 * time.Second):
		t.Errorf("daemon pid %d survived the schedule", pid)
	}
}
