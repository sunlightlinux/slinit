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
