package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cronHoldService is an internal service whose cron command marks that it
// began, then runs for secs seconds before finishing by itself.
func cronHoldService(t *testing.T, secs string) (*ServiceSet, *InternalService, string) {
	t.Helper()
	set, _ := newTestSet()
	began := filepath.Join(t.TempDir(), "began")
	svc := NewInternalService(set, "cronhold")
	svc.SetCronConfig([]string{"/bin/sh", "-c", "echo x >> " + began + "; sleep " + secs}, time.Hour, 0, "")
	set.AddService(svc)
	return set, svc, began
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Stopping a service while its cron command ran used to wait for the run,
// up to a minute, inside the state machine: PID 1 held its queue lock all
// that time and answered nothing. The service now stays in STOPPING until
// the run ends, with the lock free.
func TestCronRunDoesNotHoldQueueLockOnStop(t *testing.T) {
	set, svc, began := cronHoldService(t, "0.8")
	set.StartService(svc)
	waitFile(t, began)

	t0 := time.Now()
	set.StopService(svc)
	if took := time.Since(t0); took > 300*time.Millisecond {
		t.Fatalf("StopService took %v: it waited for the cron run", took)
	}
	if st := svc.State(); st != StateStopping {
		t.Fatalf("state during the run = %v, want STOPPING", st)
	}
	locked := make(chan struct{})
	go func() {
		set.queueMu.Lock()
		set.queueMu.Unlock() //nolint:staticcheck // probing that the lock is free
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("queue lock held while the cron run finished")
	}

	deadline := time.Now().Add(5 * time.Second)
	for svc.State() != StateStopped {
		if time.Now().After(deadline) {
			t.Fatalf("service never reached STOPPED (state %v)", svc.State())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if took := time.Since(t0); took < 500*time.Millisecond {
		t.Fatalf("STOPPED after %v, before the run could have finished", took)
	}
}

// A start while the stopped run still finishes must not wait for it either,
// and the task has to run again once that run is over. This is the on-demand
// path: a service re-armed for its next client stops its task and starts it
// again, both under the queue lock.
func TestCronStartWhileStoppedRunFinishes(t *testing.T) {
	set, _ := newTestSet()
	began := filepath.Join(t.TempDir(), "began")
	cr := NewCronRunner(NewInternalService(set, "cronagain"),
		[]string{"/bin/sh", "-c", "echo x >> " + began + "; sleep 0.5"}, time.Hour, 0, "", set.logger)
	cr.Start()
	waitFile(t, began)
	cr.signalStop()

	t0 := time.Now()
	cr.Start()
	if took := time.Since(t0); took > 100*time.Millisecond {
		t.Fatalf("Start took %v: it waited for the stopping loop", took)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(began); len(b) >= 4 { // "x\nx\n"
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the task did not run again after the stopped run finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cr.Stop()
}

// A Stop after that pending Start cancels it: no loop may outlive the Stop.
func TestCronStopCancelsPendingStart(t *testing.T) {
	set, svc, began := cronHoldService(t, "0.3")
	set.StartService(svc)
	waitFile(t, began)
	cr := svc.cronRunner
	cr.signalStop()
	cr.Start()
	cr.Stop()
	time.Sleep(200 * time.Millisecond)
	if b, _ := os.ReadFile(began); len(b) != 2 {
		t.Fatalf("task ran %d times, want 1: the cancelled start went ahead", len(b)/2)
	}
	if cr.IsRunning() {
		t.Fatal("a run is under way after Stop")
	}
}
