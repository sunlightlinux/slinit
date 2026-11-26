package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// stressLogger keeps every error, formatted, for the invariants below.
type stressLogger struct{ testLogger }

func (l *stressLogger) Error(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

// mainPIDs reads the pids the service's main processes wrote to file.
func mainPIDs(file string) []int {
	b, _ := os.ReadFile(file)
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if pid, err := strconv.Atoi(f); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

// TestStateHookInterleavingStress drives a service with state hooks
// (pre-start, pre-stop, finish, control-command, exec-condition,
// env-generator, smooth recovery: one mix per round) and a dependent
// through random starts, stops and kills, with short hook- and
// stop-timeouts, and checks, sampling under queueMu throughout:
//
//   - at most one main process alive, and it is the service's pid: a
//     run is never begun beside another;
//   - STOPPED only with no process and no hook running;
//   - no exit parked with no hook running;
//
// and, once each round has settled: STOPPED, nothing parked or queued,
// no hook goroutine left, and no error saying a hook or a start met a
// hook that was still running, or an exit went unhandled.
func TestStateHookInterleavingStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress")
	}
	base := stateHookGoroutines()
	budget := 12 * time.Second
	start := time.Now()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	r := seed
	rnd := func(n uint64) uint64 {
		r = r*6364136223846793005 + 1442695040888963407
		return (r >> 33) % n
	}
	rounds := 0
	for ; rounds < 3 || time.Since(start) < budget; rounds++ {
		stressRound(t, rounds, rnd)
		if t.Failed() {
			break
		}
	}
	t.Logf("%d rounds in %v", rounds, time.Since(start).Round(time.Millisecond))
	waitStateHooksGone(t, base, 3*time.Second)
}

func stressRound(t *testing.T, round int, rnd func(uint64) uint64) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	logger := &stressLogger{}
	set := NewServiceSet(logger)
	a := NewProcessService(set, "a")
	a.SetHookTimeout(300 * time.Millisecond)
	a.SetStopTimeout(300 * time.Millisecond)
	a.SetRestartDelay(0)
	// Each hook sleeps for 0 to 80ms, chosen by its own pid (dash has
	// no $RANDOM).
	sleepy := func(extra string) []string {
		return []string{"/bin/sh", "-c", "sleep 0.0$(($$ % 9))" + extra}
	}
	// The main process records itself, then by its pid exits at once,
	// stays, or exits cleanly a little later.
	a.SetCommand([]string{"/bin/sh", "-c", "echo $$ >> " + pidFile +
		"; case $(($$ % 3)) in 0) sleep 0.05; exit 1;; 1) exec sleep 30;; *) sleep 0.2; exit 0;; esac"})
	a.SetFinishCommand(sleepy(""))
	switch round % 3 {
	case 0:
		a.SetAutoRestart(RestartAlways)
		a.SetPreStartCommand(sleepy(""))
		a.SetPreStopHook(sleepy(""))
	case 1:
		a.SetAutoRestart(RestartAlways)
		a.SetSmoothRecovery(true)
		a.SetEnvGenerator(writeScript(t, dir, "gen", "sleep 0.0$(($$ % 9))\necho GENV=$$\n"))
	case 2:
		a.SetAutoRestart(RestartOnFailure)
		a.SetPredicates([]Predicate{{Kind: PredExecCondition,
			Param: "sleep 0.0$(($$ % 5)); [ $(($$ % 7)) -ne 0 ]"}})
		a.SetControlCommands(map[string][]string{"TERM": sleepy("; kill $1")})
		a.SetPreStopHook(sleepy(""))
	}
	b := NewProcessService(set, "b")
	b.SetCommand([]string{"/bin/sh", "-c", "trap 'sleep 0.05; exit 0' TERM; sleep 30 & wait $!; sleep 30 & wait $!"})
	set.AddService(a)
	set.AddService(b)
	if err := set.AddDependency(b, a, DepRegular); err != nil {
		t.Fatal(err)
	}

	var (
		mu       sync.Mutex
		failures []string
	)
	fail := func(format string, args ...interface{}) {
		mu.Lock()
		if len(failures) < 10 {
			failures = append(failures, fmt.Sprintf(format, args...))
		}
		mu.Unlock()
	}
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
			set.queueMu.Lock()
			st, pid, hook, pe := a.State(), a.pid, a.stateHook.Load(), a.pendingExit
			// Read under the lock: every fork happens under it, so
			// no new run can appear while we count; one may only end.
			var live []int
			for _, p := range mainPIDs(pidFile) {
				if processAlive(p) {
					live = append(live, p)
				}
			}
			set.queueMu.Unlock()
			if len(live) > 1 {
				fail("round %d: %d main processes alive: %v (service pid %d, %v)", round, len(live), live, pid, st)
			}
			if len(live) == 1 && live[0] != pid {
				fail("round %d: process %d alive but the service's pid is %d (%v)", round, live[0], pid, st)
			}
			if st == StateStopped && (pid > 0 || hook != nil) {
				fail("round %d: STOPPED with pid %d, hook %v", round, pid, hook)
			}
			if pe != nil && hook == nil {
				fail("round %d: exit of %d parked with no hook running", round, pe.PID)
			}
		}
	}()

	set.StartService(b)
	for i := 0; i < 40; i++ {
		switch rnd(6) {
		case 0, 1:
			set.StartService(a)
		case 2:
			set.StopService(a)
		case 3:
			set.StartService(b)
		case 4:
			set.StopService(b)
		case 5:
			set.queueMu.Lock()
			if a.pid > 0 {
				syscall.Kill(a.pid, syscall.SIGKILL)
			}
			set.queueMu.Unlock()
		}
		time.Sleep(time.Duration(rnd(40)) * time.Millisecond)
	}
	set.StopService(b)
	set.StopService(a)
	st := waitState(t, a, StateStopped, 5*time.Second)
	waitState(t, b, StateStopped, 3*time.Second)
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-sampled

	set.queueMu.Lock()
	if st != StateStopped || a.State() != StateStopped || a.pid > 0 ||
		a.stateHook.Load() != nil || a.pendingExit != nil || len(a.queuedHooks) > 0 {
		t.Errorf("round %d: after settling: state %v pid %d hook %v parked %v queued %d",
			round, a.State(), a.pid, a.stateHook.Load(), a.pendingExit, len(a.queuedHooks))
	}
	set.queueMu.Unlock()
	for _, p := range mainPIDs(pidFile) {
		if processAlive(p) {
			t.Errorf("round %d: main process %d outlived the stop", round, p)
			syscall.Kill(p, syscall.SIGKILL)
		}
	}
	mu.Lock()
	for _, f := range failures {
		t.Error(f)
	}
	mu.Unlock()
	logger.mu.Lock()
	for _, e := range logger.errors {
		for _, bad := range []string{"still runs", "exit not handled", "left unhandled",
			"is being stopped", "not unloaded"} {
			if strings.Contains(e, bad) {
				t.Errorf("round %d: logged %q", round, e)
			}
		}
	}
	logger.mu.Unlock()
}
