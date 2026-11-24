package service

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These timers are time.AfterFunc callbacks that take queueMu. When one
// fires while queueMu is held across a stop and a new start — an
// auto-restart, a restart — Stop() can no longer recall it, and the
// callback ran against the NEW attempt. The tests force that window by
// holding queueMu past the deadline.

func TestStaleJobTimeoutDoesNotFailTheNextAttempt(t *testing.T) {
	set, _ := newTestSet()
	s := newTestSvc(t, set, "s")
	s.autoStart = false
	s.interruptible = true
	s.SetJobTimeout(50 * time.Millisecond)
	set.AddService(s)
	set.StartService(s)

	set.queueMu.Lock()
	time.Sleep(80 * time.Millisecond) // the timer fires and waits on queueMu
	s.Stop(true)
	set.processQueuesLocked()
	if s.State() != StateStopped {
		set.queueMu.Unlock()
		t.Fatalf("not stopped: %v", s.State())
	}
	s.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(10 * time.Millisecond)
	set.queueMu.Lock()
	st, failed := s.State(), s.startFailed
	set.queueMu.Unlock()
	if st != StateStarting || failed {
		t.Fatalf("10ms into attempt 2 (job-timeout 50ms): state=%v startFailed=%v — failed by attempt 1's timer", st, failed)
	}
	// Its own timer still works.
	time.Sleep(100 * time.Millisecond)
	set.queueMu.Lock()
	st = s.State()
	set.queueMu.Unlock()
	if st != StateStopped {
		t.Errorf("attempt 2 after its own job-timeout: state=%v, want STOPPED", st)
	}
}

func TestStaleRuntimeMaxDoesNotStopTheNextRun(t *testing.T) {
	set, _ := newTestSet()
	s := newTestSvc(t, set, "s")
	s.SetRuntimeMax(50 * time.Millisecond)
	set.AddService(s)
	set.StartService(s)
	if s.State() != StateStarted {
		t.Fatalf("state %v, want STARTED", s.State())
	}

	set.queueMu.Lock()
	time.Sleep(80 * time.Millisecond)
	s.Stop(true)
	set.processQueuesLocked()
	s.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(10 * time.Millisecond)
	set.queueMu.Lock()
	st := s.State()
	set.queueMu.Unlock()
	if st != StateStarted {
		t.Fatalf("10ms into run 2 (runtime-max 50ms): state=%v — stopped by run 1's timer", st)
	}
}

// The old attempt's start-delay callback launched the new attempt with
// no delay, and the new attempt's own timer then launched a second
// process for the same start.
func TestStaleStartDelayDoesNotLaunchTwice(t *testing.T) {
	set, _ := newTestSet()
	pidfile := t.TempDir() + "/pids"
	svc := NewProcessService(set, "late")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo $$ >> " + pidfile + "; exec sleep 30"})
	svc.SetReadyNotification(3, "") // never ready: stays STARTING
	svc.SetStartDelay(50 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)

	set.queueMu.Lock()
	time.Sleep(80 * time.Millisecond)
	svc.Stop(true)
	set.processQueuesLocked()
	svc.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(400 * time.Millisecond)
	data, _ := os.ReadFile(pidfile)
	pids := strings.Fields(string(data))
	for _, p := range pids {
		if n, err := strconv.Atoi(p); err == nil {
			syscall.Kill(n, syscall.SIGKILL)
		}
	}
	set.StopService(svc)
	if len(pids) != 1 {
		t.Errorf("%d processes launched for one start: %v", len(pids), pids)
	}
}
