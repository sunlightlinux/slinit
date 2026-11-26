package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// backgroundedSleep returns a shell script that starts `sleep secs` in
// the background, writes its pid to pidFile and waits on it, so the
// sleep is a grandchild of slinit, not the direct child.
func backgroundedSleep(pidFile, secs string) []string {
	return []string{"/bin/sh", "-c", "sleep " + secs + " & echo $! > " + pidFile + "; wait"}
}

// pidFromFile waits for pidFile and returns the pid it holds, killing
// that pid at cleanup so a failing test leaves nothing behind.
func pidFromFile(t *testing.T, pidFile string) int {
	t.Helper()
	s := waitForFile(pidFile)
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		t.Fatalf("no pid in %s (%q)", pidFile, s)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// processGone reports whether pid has exited (gone or a zombie) within d.
func processGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		// state is the field after the ")" closing the comm.
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A hook that outran hook-timeout lost only its direct child: the shell
// died, the sleep it had started lived on, reparented and unwatched.
// Each hook path must take the whole process group down.
func TestHookTimeoutKillsTheProcessGroup(t *testing.T) {
	cases := []struct {
		name string
		run  func(s *ProcessService, argv []string)
	}{
		{"pre-start-command", func(s *ProcessService, argv []string) {
			s.runHookCommand(argv, "pre-start-command")
		}},
		{"finish-command", func(s *ProcessService, argv []string) {
			s.SetFinishCommand(argv)
			s.execFinishCommand(process.ChildExit{})
		}},
		{"pre-stop-hook", func(s *ProcessService, argv []string) {
			s.SetPreStopHook(argv)
			s.execPreStopHook()
		}},
		{"control-command", func(s *ProcessService, argv []string) {
			s.execControlCommand("HUP", argv)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			s := NewProcessService(set, "hookpg")
			s.SetHookTimeout(300 * time.Millisecond)
			pidFile := filepath.Join(t.TempDir(), "pid")

			t0 := time.Now()
			tc.run(s, backgroundedSleep(pidFile, "47.3"))
			if el := time.Since(t0); el > 5*time.Second {
				t.Fatalf("hook returned after %v, want about the 300ms hook-timeout", el)
			}
			pid := pidFromFile(t, pidFile)
			if !processGone(pid, 2*time.Second) {
				t.Errorf("%s timed out but its child (pid %d) is still running", tc.name, pid)
			}
		})
	}
}

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func envHas(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}

// The env-generator ran with no timeout while the start held queueMu: a
// generator that hung froze every start and stop in PID 1 (measured:
// queueMu blocked 7s by a `sleep 7.3`). It is now bounded by
// hook-timeout.
func TestEnvGeneratorBoundedByHookTimeout(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "eghang")
	s.SetHookTimeout(300 * time.Millisecond)
	s.SetEnvGenerator(writeScript(t, t.TempDir(), "gen", "echo EARLY=1\nexec sleep 7.3\n"))

	t0 := time.Now()
	env := s.buildEnv()
	if el := time.Since(t0); el > 3*time.Second {
		t.Fatalf("buildEnv took %v with a hanging env-generator; want about the 300ms hook-timeout", el)
	}
	if envHas(env, "EARLY=1") {
		t.Errorf("output of a timed-out env-generator was merged: %v", env)
	}
}

// Its stdout was read into an unbounded buffer inside PID 1. Output past
// envGeneratorMaxOutput now fails the generator.
func TestEnvGeneratorOutputCapped(t *testing.T) {
	set, logger := newTestSet()
	s := NewProcessService(set, "egbig")
	// 70000 bytes of comment lines, then a variable.
	s.SetEnvGenerator(writeScript(t, t.TempDir(), "gen",
		"i=0; while [ $i -lt 1000 ]; do echo '#"+strings.Repeat("x", 68)+"'; i=$((i+1)); done\necho CAPPED=1\n"))

	env := s.buildEnv()
	if envHas(env, "CAPPED=1") {
		t.Errorf("env-generator output past %d bytes was merged", envGeneratorMaxOutput)
	}
	logger.mu.Lock()
	logged := strings.Contains(strings.Join(logger.errors, "\n"), "env-generator")
	logger.mu.Unlock()
	if !logged {
		t.Errorf("the oversized env-generator output was not logged as a failure")
	}

	// Output under the cap still works.
	s.SetEnvGenerator(writeScript(t, t.TempDir(), "gen", "echo SMALL=1\n"))
	if env := s.buildEnv(); !envHas(env, "SMALL=1") {
		t.Errorf("small env-generator output not merged: %v", env)
	}
}

// The env-generator ran as root whatever run-as said; every other
// command slinit runs for the service runs as its user.
func TestEnvGeneratorRunsAsRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "egrunas")
	s.SetRunAs(65534, 65534)
	out := filepath.Join(dir, "uid")
	s.SetEnvGenerator(writeScript(t, dir, "gen", "id -u > "+out+"\n"))
	s.buildEnv()
	if u := waitForFile(out); u != "65534" {
		t.Errorf("env-generator ran as uid %q, want the run-as 65534", u)
	}
}

// The env-generator ran again for every hook and for every ready-check
// poll — under the scheduling lock for most of them — while the man page
// says it runs at start time. Each start now runs it once and the hooks
// of that start share the result.
func TestEnvGeneratorRunsOncePerStart(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "egonce")
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	flag := filepath.Join(dir, "ready")
	s.SetEnvGenerator(writeScript(t, dir, "gen", "echo x >> "+count+"\necho GENV=1\n"))
	s.SetCommand([]string{"/bin/sleep", "30"})
	// Not ready until the flag exists, and only with the generator's
	// variable in its environment: the cached env must still carry it.
	s.SetReadyCheckCommand([]string{"/bin/sh", "-c", `test "$GENV" = 1 && test -e ` + flag}, 30*time.Millisecond)
	s.SetPreStartCommand([]string{"/bin/sh", "-c", `test "$GENV" = 1`})
	s.SetPostStartCommand([]string{"/bin/true"})
	s.SetPreStopHook([]string{"/bin/true"})
	s.SetFinishCommand([]string{"/bin/true"})
	set.AddService(s)

	runs := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	for cycle := 1; cycle <= 2; cycle++ {
		os.Remove(flag)
		set.StartService(s)
		time.Sleep(300 * time.Millisecond) // several failing polls
		os.WriteFile(flag, nil, 0o644)
		if st := waitState(t, s, StateStarted, 5*time.Second); st != StateStarted {
			t.Fatalf("cycle %d: state %v, want STARTED", cycle, st)
		}
		set.StopService(s)
		waitState(t, s, StateStopped, 5*time.Second)
		if n := runs(); n != cycle {
			t.Fatalf("after %d start(s) with ready-check polls and hooks the env-generator ran %d times, want %d",
				cycle, n, cycle)
		}
	}
}

// ready-check-command ran with no context: a check that hung outlived
// the start-timeout failure and the stop, and every restart added one.
// The watch now kills a check under way when it ends.
func TestReadyCheckKilledWhenTheWatchEnds(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "rcleak")
	pidFile := filepath.Join(t.TempDir(), "pid")
	s.SetHookTimeout(30 * time.Second)
	s.SetReadyCheckCommand(backgroundedSleep(pidFile, "43.9"), 100*time.Millisecond)
	s.SetStartTimeout(500 * time.Millisecond)
	s.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(s)
	set.StartService(s)
	pid := pidFromFile(t, pidFile)
	if st := waitState(t, s, StateStopped, 5*time.Second); st != StateStopped {
		t.Fatalf("state %v after the start-timeout, want STOPPED", st)
	}
	if !processGone(pid, 2*time.Second) {
		t.Errorf("ready-check (pid %d) still running after the start timed out and the service stopped", pid)
	}
}

// One ready-check attempt that hung was the last: the watch waited on it
// and never polled again. Each attempt is now bounded by hook-timeout.
func TestReadyCheckAttemptBounded(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "rcbound")
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	mark := filepath.Join(dir, "tried")
	s.SetHookTimeout(300 * time.Millisecond)
	// The first attempt hangs; any later one passes.
	s.SetReadyCheckCommand([]string{"/bin/sh", "-c",
		"test -e " + mark + " && exit 0; touch " + mark + "; " + backgroundedSleep(pidFile, "44.7")[2]},
		50*time.Millisecond)
	s.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(s)
	set.StartService(s)
	pid := pidFromFile(t, pidFile)
	if st := waitState(t, s, StateStarted, 3*time.Second); st != StateStarted {
		t.Errorf("state %v: a hung ready-check attempt blocked the later ones", st)
	}
	if !processGone(pid, 2*time.Second) {
		t.Errorf("the timed-out ready-check attempt (pid %d) is still running", pid)
	}
	set.StopService(s)
	waitState(t, s, StateStopped, 5*time.Second)
}
