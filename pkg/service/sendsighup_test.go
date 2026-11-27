package service

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A process that ignores SIGTERM and exits on a hangup: with send-sighup
// the stop ends at once, on the hangup; without, at the stop timeout's
// SIGKILL.
func TestSendSIGHUPFollowsTheStopSignal(t *testing.T) {
	for _, hup := range []bool{true, false} {
		set, _ := newTestSet()
		dir := t.TempDir()
		ready := filepath.Join(dir, "ready")
		s := NewProcessService(set, "hup")
		s.SetCommand([]string{"/bin/sh", "-c",
			"trap '' TERM; trap 'exit 7' HUP; touch " + ready + "; while :; do sleep 0.05; done"})
		s.SetStopTimeout(2 * time.Second)
		s.SetSendSIGHUP(hup)
		set.AddService(s)
		set.StartService(s)
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the script never set its traps")
			}
			time.Sleep(10 * time.Millisecond)
		}
		start := time.Now()
		set.StopService(s)
		if got := waitState(t, s, StateStopped, 4*time.Second); got != StateStopped {
			t.Fatalf("hup=%v: %v", hup, got)
		}
		took := time.Since(start)
		es := s.GetExitStatus()
		if hup {
			if took > time.Second || !es.Exited() || es.ExitCode() != 7 {
				t.Errorf("send-sighup: stop took %v, exit %v; want the hangup's exit 7 at once", took, es)
			}
		} else if took < 1500*time.Millisecond || !es.Signaled() || es.Signal() != syscall.SIGKILL {
			t.Errorf("without send-sighup: stop took %v, exit %v; want SIGKILL at the stop timeout", took, es)
		}
	}
}
