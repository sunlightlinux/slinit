package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stubSlinitctl installs a fake control CLI that records its arguments,
// so the handshake can be driven without suspending the test machine.
func stubSlinitctl(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "slinitctl")
	log := filepath.Join(dir, "args")
	body := "#!/bin/sh\necho \"$@\" >> " + log + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	prev := slinitctlSuspendPath
	slinitctlSuspendPath = bin
	t.Cleanup(func() { slinitctlSuspendPath = prev })
	return log
}

// A block lock on sleep must refuse the operation outright, and must
// refuse it *before* anything is asked to suspend.
func TestSleepRefusedByBlockInhibitor(t *testing.T) {
	log := stubSlinitctl(t, 0)
	m := &manager{inhibitors: newInhibitRegistry()}
	client := addDirect(t, m.inhibitors, "sleep", "block")
	defer client.Close()

	err := m.sleep(sleepVerb{"Suspend", "mem"})
	if err == nil {
		t.Fatal("expected a block lock to refuse the suspend")
	}
	if !strings.Contains(err.Error(), "inhibited") {
		t.Errorf("error should say it was inhibited, got %q", err.Error())
	}
	// The holder has to be named, or an operator cannot find what to
	// close.
	if !strings.Contains(err.Error(), "test") || !strings.Contains(err.Error(), "4242") {
		t.Errorf("error should name the holder and its pid, got %q", err.Error())
	}
	if _, statErr := os.Stat(log); statErr == nil {
		t.Error("nothing should have been asked to suspend")
	}
}

// With nothing held, the handshake asks PID 1 through the control CLI
// and passes the kernel state for the operation.
func TestSleepRoutesThroughInit(t *testing.T) {
	log := stubSlinitctl(t, 0)
	m := &manager{inhibitors: newInhibitRegistry()}

	for _, tc := range []struct{ verb, state string }{
		{"Suspend", "mem"},
		{"Hibernate", "disk"},
	} {
		if err := m.sleep(sleepVerb{tc.verb, tc.state}); err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the control CLI was never invoked: %v", err)
	}
	got := string(data)
	for _, want := range []string{"--system suspend mem", "--system suspend disk"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in invocations:\n%s", want, got)
		}
	}
}

// A failure from PID 1 has to surface as a D-Bus error rather than a
// silent success — the shape of bug that made XFCE's Restart button log
// the user out without rebooting.
func TestSleepReportsInitFailure(t *testing.T) {
	stubSlinitctl(t, 3)
	m := &manager{inhibitors: newInhibitRegistry()}

	err := m.sleep(sleepVerb{"Suspend", "mem"})
	if err == nil {
		t.Fatal("a failing control CLI must produce an error")
	}
	if !strings.Contains(err.Error(), "suspend") {
		t.Errorf("error should name the failed operation, got %q", err.Error())
	}
}

// PreparingForSleep has to be true across the operation and false after,
// since that is what loginctl reads and what a client polls.
func TestPreparingForSleepTracksTheHandshake(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slinitctl")
	// A stub that takes its time, so the flag can be sampled while the
	// sleep is in flight.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 0.2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	prev := slinitctlSuspendPath
	slinitctlSuspendPath = bin
	t.Cleanup(func() { slinitctlSuspendPath = prev })

	m := &manager{inhibitors: newInhibitRegistry()}
	if m.preparingForSleepNow() {
		t.Fatal("should not be preparing before the call")
	}

	done := make(chan struct{})
	go func() {
		_ = m.sleep(sleepVerb{"Suspend", "mem"})
		close(done)
	}()

	// Sample mid-flight: the stub sleeps 200ms, so this lands inside.
	time.Sleep(80 * time.Millisecond)
	if !m.preparingForSleepNow() {
		t.Error("PreparingForSleep should be true while the sleep is in flight")
	}

	<-done
	if m.preparingForSleepNow() {
		t.Error("PreparingForSleep should be false once the sleep returned")
	}
}

// The flag must also clear when the operation failed, or a locker is
// left believing a sleep is still pending.
func TestPreparingForSleepClearsOnFailure(t *testing.T) {
	stubSlinitctl(t, 1)
	m := &manager{inhibitors: newInhibitRegistry()}

	if err := m.sleep(sleepVerb{"Suspend", "mem"}); err == nil {
		t.Fatal("expected failure from the stub")
	}
	if m.preparingForSleepNow() {
		t.Error("PreparingForSleep stuck true after a failed sleep")
	}
}
