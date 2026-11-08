package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cronFires(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), "x")
}

func waitFires(t *testing.T, path string, atLeast int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for cronFires(t, path) < atLeast {
		if time.Now().After(deadline) {
			t.Fatalf("%d fires, wanted %d", cronFires(t, path), atLeast)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func appendCmd(path string) []string {
	return []string{"/bin/sh", "-c", "echo x >> " + path}
}

// A service restarted used to come back with a runner that refused to start.
func TestCronRunsAgainAfterStop(t *testing.T) {
	set, l := newTestSet()
	f := filepath.Join(t.TempDir(), "n")
	cr := NewCronRunner(NewInternalService(set, "x"), appendCmd(f), 500*time.Millisecond, 0, "", l)
	cr.Start()
	waitFires(t, f, 1)
	cr.Stop()
	n := cronFires(t, f)
	cr.Start()
	waitFires(t, f, n+1)
	cr.Start() // already running: no second loop
	if !cr.active() {
		t.Error("not active")
	}
	cr.Stop()
	if cr.active() {
		t.Error("active after Stop")
	}
}

// A nightly update used to be killed after a minute.
func TestCronCalendarRunHasNoDeadline(t *testing.T) {
	set, l := newTestSet()
	spec, err := ParseCalendar("daily")
	if err != nil {
		t.Fatal(err)
	}
	cr := NewCalendarCronRunner(NewInternalService(set, "x"), []string{"/bin/true"}, spec, 0, false, "", l)
	cr.stopCh = make(chan struct{})
	ctx, cancel := cr.fireContext()
	defer cancel()
	if _, has := ctx.Deadline(); has {
		t.Error("calendar run has a deadline")
	}
	ir := NewCronRunner(NewInternalService(set, "y"), []string{"/bin/true"}, time.Minute, 0, "", l)
	ir.stopCh = make(chan struct{})
	ictx, icancel := ir.fireContext()
	defer icancel()
	if _, has := ictx.Deadline(); !has {
		t.Error("interval run lost its deadline")
	}
	origWait := cronStopWait
	cronStopWait = 50 * time.Millisecond
	defer func() { cronStopWait = origWait }()
	close(cr.stopCh)
	select {
	case <-ctx.Done():
		t.Error("stopping ended the run at once")
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Error("stopping never ended the run")
	}
}

// Stopping ends the whole group, and one that ignores SIGTERM after the grace.
func TestCronStopEndsTheProcessGroup(t *testing.T) {
	origWait, origGrace := cronStopWait, cronStopGrace
	cronStopWait, cronStopGrace = 50*time.Millisecond, 100*time.Millisecond
	defer func() { cronStopWait, cronStopGrace = origWait, origGrace }()
	set, l := newTestSet()
	spec, err := ParseCalendar("minutely")
	if err != nil {
		t.Skip(err)
	}
	for _, c := range []struct{ name, script string }{
		{"obeys SIGTERM", `sleep 100 & echo $! > PID; wait`},
		{"ignores SIGTERM", `trap "" TERM; sleep 100 & echo $! > PID; wait; sleep 100`},
	} {
		t.Run(c.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			script := strings.ReplaceAll(c.script, "PID", pidFile)
			cr := NewCalendarCronRunner(NewInternalService(set, "x"), []string{"/bin/sh", "-c", script}, spec, 0, false, "", l)
			cr.stopCh = make(chan struct{})
			done := make(chan error, 1)
			go func() { done <- cr.executeCommand() }()
			var child int
			deadline := time.Now().Add(5 * time.Second)
			for child == 0 {
				if time.Now().After(deadline) {
					t.Fatal("the command did not start")
				}
				if b, _ := os.ReadFile(pidFile); len(b) > 1 {
					child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
				}
				time.Sleep(5 * time.Millisecond)
			}
			close(cr.stopCh)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("a stopped run counted as a failure: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the run outlived the grace")
			}
			deadline = time.Now().Add(2 * time.Second)
			for syscall.Kill(child, 0) == nil {
				b, _ := os.ReadFile("/proc/" + strconv.Itoa(child) + "/stat")
				if strings.Contains(string(b), ") Z ") {
					break
				}
				if time.Now().After(deadline) {
					syscall.Kill(child, syscall.SIGKILL)
					t.Fatal("a child of the command survived")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestCronSlotReplacesARunningRunner(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	s := NewInternalService(set, "x")
	s.SetCronConfig(appendCmd(a), 500*time.Millisecond, 0, "")
	s.cronRunner.Start()
	waitFires(t, a, 1)
	first := s.cronRunner

	s.SetCronConfig(appendCmd(b), 500*time.Millisecond, 0, "")
	s.SetCronModifiers(0, false, false, 0)
	if first.active() {
		t.Error("the old runner still runs")
	}
	waitFires(t, b, 1)

	s.ClearCron()
	if s.cronRunner != nil {
		t.Error("task not removed")
	}
	n := cronFires(t, b)
	time.Sleep(600 * time.Millisecond)
	if got := cronFires(t, b); got != n {
		t.Errorf("fired after ClearCron: %d then %d", n, got)
	}
}

// A runner not running stays that way when the service is reloaded while stopped.
func TestCronSlotLeavesAStoppedRunnerStopped(t *testing.T) {
	set, _ := newTestSet()
	s := NewInternalService(set, "x")
	s.SetCronConfig([]string{"/bin/true"}, time.Hour, 0, "")
	s.SetCronConfig([]string{"/bin/true"}, time.Hour, 0, "")
	s.SetCronModifiers(0, false, false, 0)
	if s.cronRunner.active() {
		t.Error("started by a reload of a stopped service")
	}
}

func TestInternalServiceRunsCron(t *testing.T) {
	set, _ := newTestSet()
	f := filepath.Join(t.TempDir(), "n")
	s := NewInternalService(set, "periodic")
	s.SetCronConfig(appendCmd(f), 500*time.Millisecond, 0, "")
	set.StartService(s)
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("state %v", got)
	}
	waitFires(t, f, 1)
	set.StopService(s)
	if got := waitState(t, s, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state %v", got)
	}
	n := cronFires(t, f)
	time.Sleep(700 * time.Millisecond)
	if got := cronFires(t, f); got != n {
		t.Errorf("fired while stopped: %d then %d", n, got)
	}
	set.StartService(s)
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("state %v", got)
	}
	waitFires(t, f, n+1)
	set.StopService(s)
	waitState(t, s, StateStopped, 3*time.Second)
}
