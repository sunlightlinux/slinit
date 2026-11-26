package service

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startPausedSleep starts `sleep 30` under name and pauses it. At cleanup
// the process is SIGKILLed, so a failing run leaves no stopped sleep.
func startPausedSleep(t *testing.T, set *ServiceSet, name string, stopTimeout time.Duration) *ProcessService {
	t.Helper()
	svc := NewProcessService(set, name)
	svc.SetCommand([]string{"/bin/sleep", "30"})
	svc.SetStopTimeout(stopTimeout)
	set.AddService(svc)

	set.StartService(svc)
	if got := waitForState(t, svc, StateStarted, 2*time.Second); got != StateStarted {
		t.Fatalf("expected STARTED, got %v", got)
	}
	pid := svc.PID()
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		waitForState(t, svc, StateStopped, 2*time.Second)
	})
	if !svc.Pause() {
		t.Fatal("Pause() on a running service returned false")
	}
	waitProcStopped(t, pid)
	return svc
}

// waitProcStopped waits until pid is in state T. A SIGSTOP sent while the
// child is still between fork and exec is not yet a stopped process, and
// a stop issued then proves nothing.
func waitProcStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			// The state follows the parenthesised comm.
			if i := strings.LastIndexByte(string(b), ')'); i >= 0 && i+2 < len(b) && b[i+2] == 'T' {
				// The sleep exec'd under the stop; let it settle there.
				if strings.Contains(string(b), "(sleep)") {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d never reached state T after Pause", pid)
}

// A paused service must stop on its stop signal, not wait for the
// stop-timeout's SIGKILL: the SIGTERM stays pending in a SIGSTOPped process
// until something sends SIGCONT. With stop-timeout = 0 there is no SIGKILL,
// and the service used to stay STOPPING for good.
func TestPausedServiceStopsWithoutStopTimeout(t *testing.T) {
	set, _ := newTestSet()
	svc := startPausedSleep(t, set, "paused-stop", 0)

	set.StopService(svc)
	if got := waitForState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("paused service with stop-timeout = 0 did not stop: state %v", got)
	}
	if svc.IsPaused() {
		t.Error("IsPaused() still true after the stop")
	}
}

// With the default stop-timeout the stop used to take the full 10s.
func TestPausedServiceStopsPromptly(t *testing.T) {
	set, _ := newTestSet()
	svc := startPausedSleep(t, set, "paused-stop-timeout", 10*time.Second)

	begin := time.Now()
	set.StopService(svc)
	if got := waitForState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("paused service not stopped within 3s (stop-timeout 10s): state %v", got)
	}
	t.Logf("stopped in %v", time.Since(begin))
}
