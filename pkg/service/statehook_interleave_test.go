package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// The tests below are for a state hook (statehook.go) meeting the rest
// of the state machine: a start arriving while one runs, an exit
// arriving while one runs, the shutdown's final kill.

// heldHook starts a state hook of the given kind on s that runs until
// the returned release is called (or the hook is cancelled). thenCalled
// is closed by its completion.
func heldHook(t *testing.T, s *ProcessService, kind hookKind) (release func(), cancelled func() bool, thenCalled chan struct{}) {
	t.Helper()
	rel := make(chan struct{})
	thenCalled = make(chan struct{})
	var hookCtx context.Context
	s.services.queueMu.Lock()
	s.startStateHookLocked(kind,
		func(ctx context.Context) func() error {
			hookCtx = ctx
			return func() error {
				select {
				case <-rel:
				case <-ctx.Done():
				}
				return nil
			}
		},
		func(error, bool) { close(thenCalled) })
	ctx := hookCtx
	s.services.queueMu.Unlock()
	var once bool
	return func() {
			if !once {
				once = true
				close(rel)
			}
		}, func() bool {
			return ctx != nil && ctx.Err() != nil
		}, thenCalled
}

// dependentOf adds a service b that depends on a and takes half a
// second to stop, so a stop of a waits for it (waitingForDeps).
func slowDependentOf(t *testing.T, set *ServiceSet, a Service) *ProcessService {
	t.Helper()
	b := NewProcessService(set, a.Name()+"-dep")
	b.SetCommand([]string{"/bin/sh", "-c", "trap 'sleep 0.5; exit 0' TERM; sleep 30 & wait $!; sleep 30 & wait $!"})
	set.AddService(b)
	if err := set.AddDependency(b, a, DepRegular); err != nil {
		t.Fatal(err)
	}
	return b
}

func killIfAlive(pid int) {
	if pid > 0 {
		syscall.Kill(pid, syscall.SIGKILL)
	}
}

// A start that arrives while a stop waits for a dependent cancels that
// stop (CanInterruptStop). With the finish-command of the previous run
// still running, that began a new run under it, and the finish's
// completion then dispatched the old exit against the new run, which
// was stopped: the service ended STOPPED with its start lost.
func TestStartDuringFinishCommandWhileStopWaitsForDependent(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, logger := newTestSet()
	a := NewProcessService(set, "a")
	a.SetHookTimeout(10 * time.Second)
	a.SetFinishCommand(markerHook(dir, "finish", "1"))
	second := filepath.Join(dir, "second")
	log := filepath.Join(dir, "log")
	// The first run exits at once; the second stays.
	a.SetCommand([]string{"/bin/sh", "-c", "if [ -e " + second + " ]; then echo start2 >> " + log +
		"; exec sleep 30; fi; touch " + second + "; echo start >> " + log + "; sleep 0.3; exit 1"})
	set.AddService(a)
	b := slowDependentOf(t, set, a)
	set.StartService(b)
	if st := waitState(t, b, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("b %v", st)
	}
	if waitForFile(filepath.Join(dir, "finish")) == "" {
		t.Fatal("finish-command did not run")
	}
	set.StopService(a)
	set.StartService(a)
	if pid := a.PID(); pid > 0 {
		t.Errorf("a new run (pid %d) began while the finish-command still ran", pid)
	}
	// The finish-command ends, the stop ends, and the start, which
	// desired STARTED kept, brings the service back.
	if st := waitState(t, a, StateStarted, 4*time.Second); st != StateStarted {
		t.Fatalf("a %v after the finish-command, want STARTED", st)
	}
	pid := a.PID()
	defer killIfAlive(pid)
	for _, st := range statesDuring(a, 500*time.Millisecond) {
		if st != StateStarted {
			t.Errorf("a left STARTED (%v): the old exit was dispatched against the new run", st)
			break
		}
	}
	if pid <= 0 || !processAlive(pid) {
		t.Errorf("a's process %d is not running", pid)
	}
	if got := strings.Join(readLog(dir), " "); got != "start finish start2" {
		t.Errorf("order %q, want \"start finish start2\"", got)
	}
	logger.mu.Lock()
	for _, e := range logger.errors {
		if strings.Contains(e, "still runs") {
			t.Errorf("logged %q", e)
		}
	}
	logger.mu.Unlock()
	set.StopService(a)
	waitState(t, a, StateStopped, 3*time.Second)
	waitStateHooksGone(t, base, 3*time.Second)
}

// The same with a smooth recovery's env-generator: the start's
// pre-start hook superseded it, its completion never ran, and
// doingSmoothRecov stayed set.
func TestStartDuringSmoothRecoveryEnvGenWhileStopWaitsForDependent(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, logger := newTestSet()
	a := NewProcessService(set, "a")
	a.SetHookTimeout(10 * time.Second)
	a.SetSmoothRecovery(true)
	a.SetAutoRestart(RestartAlways)
	a.SetRestartDelay(0)
	count := filepath.Join(dir, "count")
	a.SetEnvGenerator(writeScript(t, dir, "gen", "echo x >> "+count+
		"\nn=$(wc -l < "+count+")\n[ \"$n\" -eq 2 ] && { echo x > "+filepath.Join(dir, "slow")+"; sleep 1.5; }\necho GENV=$n\n"))
	out := filepath.Join(dir, "out")
	a.SetCommand([]string{"/bin/sh", "-c", `echo "$GENV" >> ` + out + `; [ "$GENV" = 1 ] && exit 1; exec sleep 30`})
	set.AddService(a)
	b := slowDependentOf(t, set, a)
	set.StartService(b)
	if waitForFileWithin(filepath.Join(dir, "slow"), 5*time.Second) == "" {
		t.Fatal("the recovery did not run the env-generator")
	}
	set.StopService(a)
	set.StartService(a)
	if st := waitState(t, a, StateStarted, 4*time.Second); st != StateStarted {
		t.Fatalf("a %v, want STARTED", st)
	}
	time.Sleep(300 * time.Millisecond)
	set.queueMu.Lock()
	smooth, pid := a.doingSmoothRecov, a.pid
	set.queueMu.Unlock()
	defer killIfAlive(pid)
	if smooth {
		t.Error("doingSmoothRecov still set: the env-generator's completion never ran")
	}
	if pid <= 0 {
		t.Error("no process after the start")
	}
	bb, _ := os.ReadFile(out)
	if got := strings.Join(strings.Fields(string(bb)), " "); got != "1 3" {
		t.Errorf("runs saw GENV %q, want \"1 3\"", got)
	}
	logger.mu.Lock()
	for _, e := range logger.errors {
		if strings.Contains(e, "still runs") {
			t.Errorf("logged %q", e)
		}
	}
	logger.mu.Unlock()
	set.StopService(a)
	waitState(t, a, StateStopped, 3*time.Second)
	waitStateHooksGone(t, base, 3*time.Second)
}

// A start that cancels the stop, and then fails, took the service to
// STOPPED, where it could be unloaded, with its finish-command still
// running.
func TestNotStoppedWhileFinishCommandRuns(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetHookTimeout(10 * time.Second)
	a.SetFinishCommand(markerHook(dir, "finish", "1"))
	req := filepath.Join(dir, "req")
	if err := os.WriteFile(req, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.requiredFiles = []string{req}
	a.SetCommand([]string{"/bin/sh", "-c", "sleep 0.3; exit 1"})
	set.AddService(a)
	b := slowDependentOf(t, set, a)
	set.StartService(b)
	if waitForFile(filepath.Join(dir, "finish")) == "" {
		t.Fatal("finish-command did not run")
	}
	// The next start fails: its required file is gone.
	os.Remove(req)
	set.StopService(a)
	set.StartService(a)
	set.StopService(b)
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		set.queueMu.Lock()
		st, hook := a.State(), a.RunningStateHook()
		set.queueMu.Unlock()
		if hook == "" {
			break
		}
		if st == StateStopped {
			t.Fatalf("STOPPED while its %s still runs", hook)
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitState(t, a, StateStopped, 4*time.Second)
	waitStateHooksGone(t, base, 3*time.Second)
}

// BringUp, reached while a state hook runs, waits for it in STARTING
// and starts from its completion, rather than forking under it.
func TestBringUpWaitsForRunningStateHook(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	set.AddService(a)
	release, _, then := heldHook(t, a, hookFinish)
	defer release()
	set.StartService(a)
	time.Sleep(150 * time.Millisecond)
	if pid := a.PID(); pid > 0 {
		killIfAlive(pid)
		t.Fatalf("forked pid %d while a state hook ran", pid)
	}
	if st := a.State(); st != StateStarting {
		t.Errorf("state %v while the start waits for the hook, want STARTING", st)
	}
	release()
	<-then
	if st := waitState(t, a, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("state %v after the hook, want STARTED", st)
	}
	if pid := a.PID(); pid <= 0 {
		t.Error("no process after the hook")
	}
	set.StopService(a)
	waitState(t, a, StateStopped, 3*time.Second)
}

// A hook asked for while another runs waits for it. It used to cancel
// it, and the cancelled hook's completion never ran.
func TestStateHookDoesNotSupersedeRunningHook(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	set.AddService(a)
	releaseA, cancelledA, thenA := heldHook(t, a, hookFinish)
	defer releaseA()
	releaseB, _, thenB := heldHook(t, a, hookEnvGen)
	defer releaseB()
	time.Sleep(50 * time.Millisecond)
	if cancelledA() {
		t.Fatal("the running hook was cancelled by the next one")
	}
	select {
	case <-thenB:
		t.Fatal("the second hook ran while the first did")
	default:
	}
	if got := a.RunningStateHook(); got != "finish-command" {
		t.Errorf("running %q, want finish-command", got)
	}
	releaseA()
	select {
	case <-thenA:
	case <-time.After(2 * time.Second):
		t.Fatal("the first hook's completion did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for a.RunningStateHook() != "env-generator" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := a.RunningStateHook(); got != "env-generator" {
		t.Fatalf("running %q after the first hook, want env-generator", got)
	}
	releaseB()
	select {
	case <-thenB:
	case <-time.After(2 * time.Second):
		t.Fatal("the second hook's completion did not run")
	}
}

// Unloading a service refers its hook's completion to a service no
// longer in the set: refused while one runs.
func TestUnloadRefusedWhileStateHookRuns(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	set.AddService(a)
	release, _, then := heldHook(t, a, hookFinish)
	defer release()
	if set.UnloadServiceIfLone(a, nil) {
		t.Fatal("unloaded while a state hook ran")
	}
	release()
	<-then
	if !set.UnloadServiceIfLone(a, nil) {
		t.Fatal("not unloaded once the hook was done")
	}
}

// A stop still waiting for a dependent has not touched the process. A
// start cancelling it forked a second one, orphaning the first, which
// ran on unsupervised. The service keeps the process it has.
func TestStartCancellingStopKeepsProcess(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	a.SetPreStartCommand([]string{"/bin/true"})
	set.AddService(a)
	b := slowDependentOf(t, set, a)
	set.StartService(b)
	if st := waitState(t, b, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("b %v", st)
	}
	old := a.PID()
	defer killIfAlive(old)
	set.StopService(a)
	if st := a.State(); st != StateStopping {
		t.Fatalf("a %v after the stop, want STOPPING (waiting for b)", st)
	}
	set.StartService(a)
	time.Sleep(300 * time.Millisecond)
	nw := a.PID()
	defer killIfAlive(nw)
	if st := a.State(); st != StateStarted {
		t.Errorf("a %v, want STARTED", st)
	}
	if nw != old {
		t.Errorf("pid %d after the start, want the process the stop left running (%d); it is alive: %v",
			nw, old, processAlive(old))
	}
	if !processAlive(old) {
		t.Errorf("process %d is gone", old)
	}
	set.StopService(b)
	set.StopService(a)
	if st := waitState(t, a, StateStopped, 3*time.Second); st != StateStopped {
		t.Fatalf("a %v, want STOPPED", st)
	}
	time.Sleep(50 * time.Millisecond)
	if processAlive(old) {
		t.Errorf("process %d outlived the stop", old)
	}
}

// The same, with the process killed just after the start: it used to
// leave an exit parked during the second run's pre-start-command, which
// the next stop's pre-stop-hook took for its own, declaring the service
// STOPPED with its process running.
func TestStaleExitNotTakenByLaterStop(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetHookTimeout(5 * time.Second)
	a.SetAutoRestart(RestartAlways)
	a.SetRestartDelay(0)
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	a.SetPreStartCommand([]string{"/bin/sh", "-c", "sleep 0.3"})
	a.SetPreStopHook([]string{"/bin/sh", "-c", "sleep 0.2"})
	set.AddService(a)
	b := slowDependentOf(t, set, a)
	set.StartService(b)
	waitState(t, b, StateStarted, 3*time.Second)
	old := a.PID()
	set.StopService(a)
	set.StartService(a)
	killIfAlive(old)
	time.Sleep(100 * time.Millisecond)
	if st := waitState(t, a, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("a %v, want STARTED", st)
	}
	deadline := time.Now().Add(3 * time.Second)
	for (a.PID() <= 0 || a.PID() == old) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	nw := a.PID()
	defer killIfAlive(nw)
	set.queueMu.Lock()
	pe := a.pendingExit
	set.queueMu.Unlock()
	if pe != nil {
		t.Errorf("exit of %d still parked with the service running", pe.PID)
	}
	set.StopService(b)
	set.StopService(a)
	waitState(t, a, StateStopped, 3*time.Second)
	if a.State() == StateStopped && processAlive(nw) {
		t.Errorf("STOPPED while its process %d still runs", nw)
	}
}

// An exit is parked only for a stop's hooks, and only that process's.
func TestExitNotParkedForOtherHooks(t *testing.T) {
	set, logger := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	set.AddService(a)
	set.StartService(a)
	if st := waitState(t, a, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("a %v", st)
	}
	pid := a.PID()
	// Not expected in use (no other hook runs beside a process); this
	// is the guard for it.
	release, _, then := heldHook(t, a, hookPreStart)
	defer release()
	killIfAlive(pid)
	deadline := time.Now().Add(2 * time.Second)
	for a.PID() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	set.queueMu.Lock()
	pe := a.pendingExit
	set.queueMu.Unlock()
	if pe != nil {
		t.Errorf("exit of %d parked during a %s; nothing takes it", pe.PID, hookPreStart)
	}
	release()
	<-then
	logger.mu.Lock()
	found := false
	for _, e := range logger.errors {
		found = found || strings.Contains(e, "exit not handled")
	}
	logger.mu.Unlock()
	if !found {
		t.Error("the unexpected exit was not logged")
	}
}

// A new run starts with no parked exit, whatever the last one left.
func TestStartProcessClearsParkedExit(t *testing.T) {
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	set.AddService(a)
	set.queueMu.Lock()
	a.pendingExit = &process.ChildExit{PID: 1 << 22}
	set.queueMu.Unlock()
	set.StartService(a)
	if st := waitState(t, a, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("a %v", st)
	}
	defer killIfAlive(a.PID())
	set.queueMu.Lock()
	pe := a.pendingExit
	set.queueMu.Unlock()
	if pe != nil {
		t.Errorf("the new run kept a parked exit (pid %d)", pe.PID)
	}
	set.StopService(a)
	waitState(t, a, StateStopped, 3*time.Second)
}

// After the shutdown's final kill, the completion of a pre-stop-hook it
// ended started the finish-command, which nothing ended: the teardown
// waited up to hook-timeout for it.
func TestNoStateHookAfterFinalKill(t *testing.T) {
	base := stateHookGoroutines()
	dir := t.TempDir()
	set, _ := newTestSet()
	a := NewProcessService(set, "a")
	a.SetHookTimeout(10 * time.Second)
	a.SetCommand([]string{"/bin/sh", "-c", "exec sleep 30"})
	a.SetPreStopHook([]string{"/bin/sh", "-c", "sleep 5"})
	a.SetFinishCommand(markerHook(dir, "finish", "3"))
	set.AddService(a)
	set.StartService(a)
	if st := waitState(t, a, StateStarted, 3*time.Second); st != StateStarted {
		t.Fatalf("a %v", st)
	}
	pid := a.PID()
	set.StopService(a)
	time.Sleep(200 * time.Millisecond)
	killIfAlive(pid) // exits during the pre-stop-hook: parked
	time.Sleep(200 * time.Millisecond)
	set.KillActiveServices()
	if st := waitState(t, a, StateStopped, time.Second); st != StateStopped {
		t.Errorf("a %v a second after the final kill (running %q), want STOPPED", st, a.RunningStateHook())
	}
	if _, err := os.Stat(filepath.Join(dir, "finish")); err == nil {
		t.Error("finish-command ran after the final kill")
	}
	waitStateHooksGone(t, base, 4*time.Second)
}
