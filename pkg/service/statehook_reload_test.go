package service

import (
	"path/filepath"
	"testing"
	"time"
)

// A reload refuses a service whose state hook is running: the hook reads
// the service's settings off the lock (statehook.go).
func TestRunningStateHookNamesTheHook(t *testing.T) {
	dir := t.TempDir()
	set, _ := newTestSet()
	s := NewProcessService(set, "rsh")
	s.SetHookTimeout(10 * time.Second)
	s.SetPreStartCommand(markerHook(dir, "prestart", "30"))
	s.SetCommand([]string{"/bin/sleep", "30"})
	set.AddService(s)
	if got := s.RunningStateHook(); got != "" {
		t.Errorf("RunningStateHook = %q with nothing running", got)
	}
	go set.StartService(s)
	if waitForFile(filepath.Join(dir, "prestart")) == "" {
		t.Fatal("pre-start-command did not run")
	}
	set.queueMu.Lock()
	got := s.RunningStateHook()
	set.queueMu.Unlock()
	if got != "pre-start-command" {
		t.Errorf("RunningStateHook = %q, want pre-start-command", got)
	}
	set.StopService(s)
	waitState(t, s, StateStopped, 3*time.Second)
	if got := s.RunningStateHook(); got != "" {
		t.Errorf("RunningStateHook = %q after the stop", got)
	}
}
