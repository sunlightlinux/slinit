package service

import (
	"syscall"
	"testing"
	"time"
)

// A daemon that ignores SIGTERM is SIGKILLed at the stop-timeout even when
// it does not lead a process group of its own: one backgrounded by a shell
// launcher without setsid stays in the launcher's group. The escalation
// used to signal the group numbered after the daemon's pid, which then does
// not exist, so the kill went nowhere and the service stayed STOPPING for
// good. dinit's kill_pg looks the group up with getpgid.
func TestBGProcessStopTimeoutKillsNonLeaderDaemon(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewBGProcessService(set, "d")
	svc.SetCommand(trappingDaemon(t, dir, "TERM:", "INT:"))
	svc.SetStopTimeout(400 * time.Millisecond)
	daemon := startBgStopTest(t, set, svc, dir)
	if pgid, err := syscall.Getpgid(daemon); err != nil || pgid == daemon {
		t.Fatalf("daemon %d leads its own group (pgid %d, %v); the case needs one that does not", daemon, pgid, err)
	}

	stopWithin(t, set, svc, 5*time.Second)
	if processAlive(daemon) {
		t.Errorf("daemon %d survived the stop-timeout", daemon)
	}
}

// A hung stop-command and a daemon that ignores SIGTERM: the stop-timeout
// SIGKILLs both and the service reaches STOPPED.
func TestBgStopCommandHangsStopTimeoutKillsAll(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewBGProcessService(set, "d")
	svc.SetCommand(trappingDaemon(t, dir, "TERM:", "INT:"))
	svc.SetStopCommand([]string{"/bin/sh", "-c",
		"echo $$ > " + dir + "/stop.pid; exec sleep 60"})
	svc.SetStopTimeout(400 * time.Millisecond)
	daemon := startBgStopTest(t, set, svc, dir)

	stopWithin(t, set, svc, 5*time.Second)
	stopPID := waitPIDFile(t, dir+"/stop.pid")
	defer killIfAlive(stopPID)
	if processAlive(daemon) {
		t.Errorf("daemon %d survived the stop-timeout", daemon)
	}
	if processAlive(stopPID) {
		t.Errorf("stop-command %d survived the stop-timeout", stopPID)
	}
}
