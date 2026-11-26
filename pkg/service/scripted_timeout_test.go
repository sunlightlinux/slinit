package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A start-command that overruns start-timeout and ignores the SIGINT sent
// then is SIGKILLed at the stop-timeout; the start is reported failed and
// the service ends STOPPED.
func TestScriptedStartTimeoutEscalatesPastIgnoredSIGINT(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewScriptedService(set, "sc")
	svc.SetStartCommand([]string{"/bin/sh", "-c",
		"trap '' INT; echo $$ > " + dir + "/start.pid; while :; do sleep 0.05; done"})
	svc.SetStartTimeout(300 * time.Millisecond)
	svc.SetStopTimeout(300 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)
	pid := waitPIDFile(t, dir+"/start.pid")
	defer killIfAlive(pid)

	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}
	if !svc.DidStartFail() {
		t.Error("a timed-out start was not reported as failed")
	}
	if processAlive(pid) {
		t.Errorf("start-command %d survived the stop-timeout", pid)
	}
}

// A stop-command that hangs is SIGKILLed at the stop-timeout and the
// service still reaches STOPPED.
func TestScriptedStopCommandHangsIsKilled(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewScriptedService(set, "sc")
	svc.SetStartCommand([]string{"/bin/true"})
	svc.SetStopCommand([]string{"/bin/sh", "-c",
		"echo $$ > " + dir + "/stop.pid; exec sleep 60"})
	svc.SetStopTimeout(300 * time.Millisecond)
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	stopWithin(t, set, svc, 3*time.Second)
	pid := waitPIDFile(t, dir+"/stop.pid")
	defer killIfAlive(pid)
	if processAlive(pid) {
		t.Errorf("stop-command %d survived the stop-timeout", pid)
	}
}

// log-type = file: the start-command's output lands in the logfile.
func TestScriptedStartCommandOutputToLogFile(t *testing.T) {
	set, _ := newTestSet()
	log := t.TempDir() + "/out.log"
	svc := NewScriptedService(set, "sc")
	svc.SetStartCommand([]string{"/bin/sh", "-c", "echo scripted-out"})
	svc.SetLogType(LogToFile)
	svc.SetLogFileDetails(log, 0o600, -1, -1)
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	b, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(b), "scripted-out") {
		t.Errorf("logfile = %q (%v), want the start-command's output", b, err)
	}
}

// log-type = buffer: the start-command's output is kept in the log buffer.
func TestScriptedStartCommandOutputToBuffer(t *testing.T) {
	set, _ := newTestSet()
	svc := NewScriptedService(set, "sc")
	svc.SetStartCommand([]string{"/bin/sh", "-c", "echo scripted-buf"})
	svc.SetLogType(LogToBuffer)
	set.AddService(svc)
	set.StartService(svc)
	mustReachState(t, svc, StateStarted)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if lb := svc.GetLogBuffer(); lb != nil && strings.Contains(string(lb.GetBuffer()), "scripted-buf") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the start-command's output never reached the log buffer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// required-files is checked before the start-command runs.
func TestScriptedRequiredPathMissingRunsNothing(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	svc := NewScriptedService(set, "sc")
	svc.SetStartCommand([]string{"/bin/sh", "-c", "touch " + dir + "/ran"})
	svc.Record().SetRequiredPaths(nil, []string{dir + "/missing"})
	set.AddService(svc)
	set.StartService(svc)

	if got := waitState(t, svc, StateStopped, 3*time.Second); got != StateStopped {
		t.Fatalf("state = %v, want STOPPED", got)
	}
	if !svc.DidStartFail() {
		t.Error("a missing required directory was not a failed start")
	}
	time.Sleep(100 * time.Millisecond)
	if pathExists(dir + "/ran") {
		t.Error("the start-command ran although a required directory was missing")
	}
}
