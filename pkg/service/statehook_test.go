package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The hooks a transition waits on ran under queueMu, freezing every
// other transition and the control socket for as long as they ran
// (statehook.go). Each test below starts a hook that sleeps for two
// seconds, waits until it is running, and then requires queueMu to be
// free within 200ms.

const hookSleep = "2"

// queueMuWait takes and releases queueMu, giving up the wait after
// limit. The lock is still taken and given back later in that case, so
// a held lock is not left with a waiter that never returns.
func queueMuWait(set *ServiceSet, limit time.Duration) (time.Duration, bool) {
	got := make(chan time.Duration, 1)
	t0 := time.Now()
	go func() {
		set.queueMu.Lock()
		got <- time.Since(t0)
		set.queueMu.Unlock()
	}()
	select {
	case d := <-got:
		return d, true
	case <-time.After(limit):
		return limit, false
	}
}

// requireQueueMuFree fails the test unless queueMu can be taken within
// 200ms.
func requireQueueMuFree(t *testing.T, set *ServiceSet, what string) {
	t.Helper()
	if d, ok := queueMuWait(set, 200*time.Millisecond); !ok {
		t.Errorf("%s: queueMu still held after %v — the hook runs under the scheduling lock", what, d)
	}
}

// markerHook is a hook that records its arguments (or "ran") in marker, appends
// name to the shared log, and then sleeps.
func markerHook(dir, name, sleep string) []string {
	return []string{"/bin/sh", "-c",
		`echo "${*:-ran}" > ` + filepath.Join(dir, name) + `; echo ` + name + ` >> ` +
			filepath.Join(dir, "log") + `; sleep ` + sleep, "hook"}
}

// readLog returns the lines of dir/log.
func readLog(dir string) []string {
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	return strings.Fields(string(b))
}

// stateHookGoroutines counts the state-hook goroutines alive now.
func stateHookGoroutines() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "startStateHookLocked")
}

// waitStateHooksGone fails the test if more state-hook goroutines than
// base (counted when it began) are alive after within: nothing may
// outlive the transition it belongs to.
func waitStateHooksGone(t *testing.T, base int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		c := stateHookGoroutines()
		if c <= base {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%d state-hook goroutine(s) still running after %v", c-base, within)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stateDuring samples s's state until d has passed and returns every
// distinct state seen.
func statesDuring(s Service, d time.Duration) []ServiceState {
	var seen []ServiceState
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		st := s.State()
		if len(seen) == 0 || seen[len(seen)-1] != st {
			seen = append(seen, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return seen
}

func TestFinishCommandRunsOffQueueMu(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "fin")
	s.SetHookTimeout(10 * time.Second)
	s.SetFinishCommand(markerHook(dir, "finish", hookSleep))
	s.SetCommand([]string{"/bin/sh", "-c", "exit 3"})
	set.AddService(s)
	set.StartService(s)
	if waitForFile(filepath.Join(dir, "finish")) == "" {
		t.Fatal("finish-command did not run")
	}
	requireQueueMuFree(t, set, "finish-command")
	// The exit is handled after the finish-command, not before: the
	// service has not settled while it runs.
	if st := s.State(); st == StateStopped {
		t.Errorf("service STOPPED while its finish-command still runs")
	}
	if st := waitState(t, s, StateStopped, 5*time.Second); st != StateStopped {
		t.Fatalf("state %v after the finish-command, want STOPPED", st)
	}
	if got := waitForFile(filepath.Join(dir, "finish")); got != "3 0" {
		t.Errorf("finish-command got arguments %q, want \"3 0\"", got)
	}
	waitStateHooksGone(t, base, 2*time.Second)
}

// systemd runs ExecStopPost= before the restart: the finish-command of
// one run comes before the next run's start.
func TestFinishCommandRunsBeforeRestart(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "finr")
	s.SetHookTimeout(10 * time.Second)
	s.SetAutoRestart(RestartAlways)
	s.SetRestartDelay(0)
	s.SetFinishCommand(markerHook(dir, "finish", "0.3"))
	s.SetCommand([]string{"/bin/sh", "-c", "echo start >> " + filepath.Join(dir, "log") + "; exit 1"})
	set.AddService(s)
	set.StartService(s)
	deadline := time.Now().Add(5 * time.Second)
	for len(readLog(dir)) < 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	set.StopService(s)
	waitState(t, s, StateStopped, 5*time.Second)
	waitStateHooksGone(t, base, 3*time.Second)
	lines := readLog(dir)
	if len(lines) < 5 {
		t.Fatalf("only %d runs logged: %v", len(lines), lines)
	}
	for i, l := range lines {
		want := "start"
		if i%2 == 1 {
			want = "finish"
		}
		if l != want {
			t.Fatalf("line %d is %q, want %q: %v", i, l, want, lines)
		}
	}
}

// A stop that arrives while the finish-command runs waits for it, and
// ends the service rather than letting the exit restart it.
func TestStopDuringFinishCommand(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "fins")
	s.SetHookTimeout(10 * time.Second)
	s.SetAutoRestart(RestartAlways)
	s.SetRestartDelay(0)
	s.SetFinishCommand(markerHook(dir, "finish", "1"))
	s.SetCommand([]string{"/bin/sh", "-c", "echo start >> " + filepath.Join(dir, "log") + "; sleep 0.3"})
	set.AddService(s)
	set.StartService(s)
	if waitForFile(filepath.Join(dir, "finish")) == "" {
		t.Fatal("finish-command did not run")
	}
	done := make(chan struct{})
	go func() {
		set.StopService(s)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Error("StopService blocked behind the finish-command")
	}
	seen := statesDuring(s, 600*time.Millisecond)
	for _, st := range seen {
		if st == StateStopped {
			t.Errorf("STOPPED while the finish-command still ran (states %v)", seen)
		}
	}
	if st := waitState(t, s, StateStopped, 5*time.Second); st != StateStopped {
		t.Fatalf("state %v, want STOPPED", st)
	}
	time.Sleep(300 * time.Millisecond)
	if st := s.State(); st != StateStopped {
		t.Errorf("state %v after the stop, want STOPPED (restarted?)", st)
	}
	if n := strings.Count(strings.Join(readLog(dir), " "), "start"); n != 1 {
		t.Errorf("%d starts, want 1: %v", n, readLog(dir))
	}
	waitStateHooksGone(t, base, 3*time.Second)
}

func TestPreStopHookRunsOffQueueMu(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "pstop")
	s.SetHookTimeout(10 * time.Second)
	s.SetPreStopHook(markerHook(dir, "prestop", hookSleep))
	s.SetFinishCommand(markerHook(dir, "finish", "0"))
	s.SetCommand([]string{"/bin/sh", "-c", "trap 'echo term >> " + filepath.Join(dir, "log") +
		"; exit 0' TERM; echo start >> " + filepath.Join(dir, "log") + "; sleep 30 & wait $!"})
	set.AddService(s)
	set.StartService(s)
	if st := waitState(t, s, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v, want STARTED", st)
	}
	pid := s.PID()
	// The trap is set once the process has logged.
	for d := time.Now().Add(3 * time.Second); len(readLog(dir)) == 0 && time.Now().Before(d); {
		time.Sleep(10 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() {
		set.StopService(s)
		close(done)
	}()
	if waitForFile(filepath.Join(dir, "prestop")) == "" {
		t.Fatal("pre-stop-hook did not run")
	}
	requireQueueMuFree(t, set, "pre-stop-hook")
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Error("StopService blocked behind the pre-stop-hook")
	}
	if st := s.State(); st != StateStopping {
		t.Errorf("state %v while the pre-stop-hook runs, want STOPPING", st)
	}
	if st := waitState(t, s, StateStopped, 6*time.Second); st != StateStopped {
		t.Fatalf("state %v, want STOPPED", st)
	}
	if got := waitForFile(filepath.Join(dir, "prestop")); got != strconv.Itoa(pid) {
		t.Errorf("pre-stop-hook got %q, want the pid %d", got, pid)
	}
	// Order: the hook before the stop signal, the finish-command last.
	if got := strings.Join(readLog(dir), " "); got != "start prestop term finish" {
		t.Errorf("order %q, want \"start prestop term finish\"", got)
	}
	waitStateHooksGone(t, base, 2*time.Second)
}

// control-command-TERM stands in for the stop signal; it ran under the
// lock too.
func TestStopControlCommandRunsOffQueueMu(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "pctl")
	s.SetHookTimeout(10 * time.Second)
	// The command records its pid argument, sleeps, then does the
	// stop itself.
	s.SetControlCommands(map[string][]string{"TERM": {"/bin/sh", "-c",
		`echo "$1" > ` + filepath.Join(dir, "ctl") + `; echo ctl >> ` + filepath.Join(dir, "log") +
			`; sleep ` + hookSleep + `; kill "$1"`, "ctl"}})
	s.SetFinishCommand(markerHook(dir, "finish", "0"))
	s.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(s)
	set.StartService(s)
	if st := waitState(t, s, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v, want STARTED", st)
	}
	go set.StopService(s)
	if waitForFile(filepath.Join(dir, "ctl")) == "" {
		t.Fatal("control-command did not run")
	}
	requireQueueMuFree(t, set, "control-command-TERM")
	if st := waitState(t, s, StateStopped, 6*time.Second); st != StateStopped {
		t.Fatalf("state %v, want STOPPED", st)
	}
	if got := strings.Join(readLog(dir), " "); got != "ctl finish" {
		t.Errorf("order %q, want \"ctl finish\"", got)
	}
	waitStateHooksGone(t, base, 2*time.Second)
}

// A start asked for while the pre-stop-hook runs does not cut the stop
// short: the stop completes (hook, signal, exit, finish-command) and the
// service then starts again.
func TestStartDuringPreStopHook(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "pstart")
	s.SetHookTimeout(10 * time.Second)
	s.SetPreStopHook(markerHook(dir, "prestop", "0.5"))
	s.SetFinishCommand(markerHook(dir, "finish", "0"))
	s.SetCommand([]string{"/bin/sh", "-c", "echo start >> " + filepath.Join(dir, "log") + "; exec sleep 30"})
	set.AddService(s)
	set.StartService(s)
	if st := waitState(t, s, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v, want STARTED", st)
	}
	// STARTED comes at the fork; wait for the process to have logged.
	for d := time.Now().Add(3 * time.Second); len(readLog(dir)) == 0 && time.Now().Before(d); {
		time.Sleep(10 * time.Millisecond)
	}
	go set.StopService(s)
	if waitForFile(filepath.Join(dir, "prestop")) == "" {
		t.Fatal("pre-stop-hook did not run")
	}
	set.StartService(s)
	if st := s.State(); st != StateStopping {
		t.Errorf("state %v right after the start, want STOPPING (pre-stop-hook still runs)", st)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(readLog(dir)) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := waitState(t, s, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v, want STARTED again", st)
	}
	if got := strings.Join(readLog(dir), " "); got != "start prestop finish start" {
		t.Errorf("order %q, want \"start prestop finish start\"", got)
	}
	set.StopService(s)
	waitState(t, s, StateStopped, 5*time.Second)
	waitStateHooksGone(t, base, 3*time.Second)
}

// The main process dies on its own while the pre-stop-hook runs: the
// exit waits for the hook, then the finish-command runs and the stop
// completes, with no signal sent to a pid the service no longer owns.
func TestExitDuringPreStopHook(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "pexit")
	s.SetHookTimeout(10 * time.Second)
	s.SetPreStopHook([]string{"/bin/sh", "-c", `echo prestop >> ` + filepath.Join(dir, "log") +
		`; kill "$1"; sleep 0.5; echo prestop-end >> ` + filepath.Join(dir, "log"), "hook"})
	s.SetFinishCommand(markerHook(dir, "finish", "0"))
	s.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(s)
	set.StartService(s)
	if st := waitState(t, s, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v, want STARTED", st)
	}
	set.StopService(s)
	time.Sleep(250 * time.Millisecond)
	if st := s.State(); st != StateStopping {
		t.Errorf("state %v while the pre-stop-hook runs, want STOPPING", st)
	}
	if st := waitState(t, s, StateStopped, 5*time.Second); st != StateStopped {
		t.Fatalf("state %v, want STOPPED", st)
	}
	if got := strings.Join(readLog(dir), " "); got != "prestop prestop-end finish" {
		t.Errorf("order %q, want \"prestop prestop-end finish\"", got)
	}
	waitStateHooksGone(t, base, 2*time.Second)
}
