package service

import (
	"os"
	"testing"
	"time"
)

// A scripted job a condition skipped runs no stop-command at its stop.
func TestScriptedSkippedNoStopCommand(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	j := NewScriptedService(set, "skipjob")
	j.SetStartCommand([]string{"/bin/true"})
	j.SetStopCommand([]string{"/bin/sh", "-c", "touch " + dir + "/stopped"})
	j.Record().SetPredicates([]Predicate{{Kind: PredPathExists, Param: dir + "/never"}})
	set.AddService(j)
	set.StartService(j)
	waitState(t, j, StateStarted, 3*time.Second)
	set.StopService(j)
	waitState(t, j, StateStopped, 3*time.Second)
	if _, err := os.Stat(dir + "/stopped"); err == nil {
		t.Error("a skipped job's stop ran its stop-command")
	}
}
