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
	s.SetJobTimeout(300 * time.Millisecond)
	set.AddService(s)
	set.StartService(s)

	set.queueMu.Lock()
	time.Sleep(400 * time.Millisecond) // the timer fires and waits on queueMu
	s.Stop(true)
	set.processQueuesLocked()
	if s.State() != StateStopped {
		set.queueMu.Unlock()
		t.Fatalf("not stopped: %v", s.State())
	}
	s.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(50 * time.Millisecond)
	set.queueMu.Lock()
	st, failed := s.State(), s.startFailed
	set.queueMu.Unlock()
	if st != StateStarting || failed {
		t.Fatalf("50ms into attempt 2 (job-timeout 300ms): state=%v startFailed=%v — failed by attempt 1's timer", st, failed)
	}
	// Its own timer still works.
	time.Sleep(600 * time.Millisecond)
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
	s.SetRuntimeMax(300 * time.Millisecond)
	set.AddService(s)
	set.StartService(s)
	if s.State() != StateStarted {
		t.Fatalf("state %v, want STARTED", s.State())
	}

	set.queueMu.Lock()
	time.Sleep(400 * time.Millisecond)
	s.Stop(true)
	set.processQueuesLocked()
	s.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(50 * time.Millisecond)
	set.queueMu.Lock()
	st := s.State()
	set.queueMu.Unlock()
	if st != StateStarted {
		t.Fatalf("50ms into run 2 (runtime-max 300ms): state=%v — stopped by run 1's timer", st)
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
	svc.SetStartDelay(300 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)

	set.queueMu.Lock()
	time.Sleep(400 * time.Millisecond)
	svc.Stop(true)
	set.processQueuesLocked()
	svc.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(800 * time.Millisecond)
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

// The process timer is a time.Timer read by the monitor goroutine. A
// start-timeout tick the monitor has already received, while it waits
// for queueMu held across a stop, was handled with the purpose the stop
// had just armed: the stop-timeout escalation ran at once and SIGKILLed
// a service that had its whole stop-timeout still to go.

// ignoresInt is a start that never signals ready and ignores the
// SIGINT that cancels it, and the SIGTERM of a stop.
var ignoresInt = []string{"/bin/sh", "-c", "trap '' TERM INT; while :; do sleep 0.05; done"}

// stopsWithin waits up to d for svc to reach STOPPED.
func stopsWithin(svc Service, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if svc.State() == StateStopped {
			return true
		}
	}
	return false
}

func TestStaleStartTimeoutTickIsNotTheStopTimeout(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "slowstart")
	svc.SetCommand(ignoresInt)
	svc.SetReadyNotification(3, "")
	svc.SetStartTimeout(300 * time.Millisecond)
	svc.SetStopTimeout(3 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	time.Sleep(100 * time.Millisecond)
	defer func() { set.ForceStopService(svc); stopsWithin(svc, 4*time.Second) }()

	set.queueMu.Lock()
	time.Sleep(400 * time.Millisecond) // the monitor takes the tick and waits on queueMu
	svc.Stop(true)
	set.processQueuesLocked()
	set.queueMu.Unlock()

	if stopsWithin(svc, 1500*time.Millisecond) {
		t.Error("stopped within 1.5s of the stop request: the stale start-timeout tick ran the stop-timeout escalation (stop-timeout is 3s)")
	}
}

func TestScriptedStaleStartTimeoutTickIsNotTheStopTimeout(t *testing.T) {
	set, _ := newTestSet()
	svc := NewScriptedService(set, "slowstart")
	svc.SetStartCommand(ignoresInt)
	svc.Flags.StartInterruptible = true
	svc.SetStartTimeout(300 * time.Millisecond)
	svc.SetStopTimeout(3 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	time.Sleep(100 * time.Millisecond)
	defer func() { set.ForceStopService(svc); stopsWithin(svc, 4*time.Second) }()

	set.queueMu.Lock()
	time.Sleep(400 * time.Millisecond)
	svc.Stop(true)
	set.processQueuesLocked()
	set.queueMu.Unlock()

	if stopsWithin(svc, 1500*time.Millisecond) {
		t.Error("stopped within 1.5s of the stop request: the stale start-timeout tick ran the stop-timeout escalation (stop-timeout is 3s)")
	}
}

func TestBGProcessStaleStartTimeoutTickIsNotTheStopTimeout(t *testing.T) {
	set, _ := newTestSet()
	svc := NewBGProcessService(set, "slowstart")
	svc.SetCommand(ignoresInt)
	svc.SetPIDFile(filepath.Join(t.TempDir(), "never.pid"))
	svc.SetStartTimeout(300 * time.Millisecond)
	svc.SetStopTimeout(3 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	time.Sleep(100 * time.Millisecond)
	defer func() { set.ForceStopService(svc); stopsWithin(svc, 4*time.Second) }()

	set.queueMu.Lock()
	time.Sleep(400 * time.Millisecond)
	svc.Stop(true)
	set.processQueuesLocked()
	set.queueMu.Unlock()

	if stopsWithin(svc, 1500*time.Millisecond) {
		t.Error("stopped within 1.5s of the stop request: the stale start-timeout tick ran the stop-timeout escalation (stop-timeout is 3s)")
	}
}

// A watchdog expiry already waiting on queueMu when its watcher is
// cancelled — as handleChildExit and BringDown do before a restart —
// stopped the service all the same, the next instance included.
func TestStaleWatchdogFireAfterCancelDoesNotStop(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "wd")
	svc.SetCommand([]string{"/bin/sh", "-c", "echo r >&3; exec sleep 60"})
	svc.SetReadyNotification(3, "")
	svc.SetWatchdogTimeout(300 * time.Millisecond)
	svc.SetStartTimeout(2 * time.Second)
	set.AddService(svc)
	set.StartService(svc)
	for end := time.Now().Add(2 * time.Second); svc.State() != StateStarted && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	if svc.State() != StateStarted {
		t.Fatalf("setup: %v, want STARTED", svc.State())
	}
	defer func() { set.ForceStopService(svc); stopsWithin(svc, 2*time.Second) }()

	set.queueMu.Lock()
	time.Sleep(600 * time.Millisecond) // the deadline passes; the watcher waits on queueMu
	svc.stopWatchdogWatcherLocked()
	set.queueMu.Unlock()

	time.Sleep(300 * time.Millisecond)
	if st := svc.State(); st != StateStarted {
		t.Errorf("state %v after the watcher was cancelled: the stale watchdog fire stopped the service", st)
	}
}

// The same for the OOM watcher: an OOM kill seen by the watcher of an
// instance stopped and started again while it waited on queueMu stopped
// the new instance.
func TestStaleOOMWatcherDoesNotStopTheNextRun(t *testing.T) {
	restore := setOOMPollIntervalForTesting(30 * time.Millisecond)
	defer restore()

	set, _ := newTestSet()
	svc := NewInternalService(set, "oom")
	set.AddService(svc)
	cgDir := t.TempDir()
	eventsPath := filepath.Join(cgDir, "memory.events")
	writeMemoryEvents(t, eventsPath, 0, 0)
	svc.Record().SetCgroupPath(cgDir)
	svc.Record().SetOOMPolicy(OOMStop)
	set.StartService(svc)

	set.queueMu.Lock()
	writeMemoryEvents(t, eventsPath, 1, 0)
	time.Sleep(200 * time.Millisecond) // the watcher sees it and waits on queueMu
	svc.Stop(true)
	set.processQueuesLocked()
	if svc.State() != StateStopped {
		set.queueMu.Unlock()
		t.Fatalf("not stopped: %v", svc.State())
	}
	svc.Start()
	set.processQueuesLocked()
	set.queueMu.Unlock()

	time.Sleep(200 * time.Millisecond)
	if st := svc.State(); st != StateStarted {
		t.Errorf("state %v: the first run's OOM watcher stopped the next run", st)
	}
}
