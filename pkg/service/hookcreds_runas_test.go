package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// These ran as root whatever run-as said. Before the fix each one
// reported uid 0 here.

func reportUID(dir, name string) []string {
	return []string{"/bin/sh", "-c", "id -u > " + filepath.Join(dir, name) + "; exit 1"}
}

func wantRunAsUID(t *testing.T, dir, name string) {
	t.Helper()
	if got := waitForFile(filepath.Join(dir, name)); got != "65534" {
		t.Errorf("%s ran as uid %q; want 65534 (run-as)", name, got)
	}
}

func TestCronCommandHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRunAs(65534, 65534)
	cr := NewCronRunner(s, reportUID(dir, "cron-command"), time.Second, 0, "", set.logger)
	_ = cr.executeCommand()
	wantRunAsUID(t, dir, "cron-command")
}

func TestHealthCheckCommandHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRunAs(65534, 65534)
	hc := NewHealthChecker(s, reportUID(dir, "health-check-command"), time.Second, 0, 0,
		nil, set.logger, nil)
	hc.checkOnce(context.Background())
	wantRunAsUID(t, dir, "health-check-command")
}

func TestUnhealthyCommandHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetRunAs(65534, 65534)
	hc := NewHealthChecker(s, []string{"/bin/false"}, time.Second, 0, 0,
		reportUID(dir, "unhealthy-command"), set.logger, nil)
	hc.runUnhealthyCmd(context.Background())
	wantRunAsUID(t, dir, "unhealthy-command")
}

// exec-condition, through CheckPredicates as BringUp evaluates it (with
// queueMu held), for each type that has run-as.
func TestExecConditionHonoursRunAs(t *testing.T) {
	if !inRootUserNS(t) {
		return
	}
	dir := worldWritableDir(t)
	set, _ := newTestSet()
	cond := func(name string) []Predicate {
		return []Predicate{{Kind: PredExecCondition,
			Param: "id -u > " + filepath.Join(dir, name)}}
	}

	p := NewProcessService(set, "p")
	p.SetRunAs(65534, 65534)
	p.SetPredicates(cond("exec-condition-process"))
	sc := NewScriptedService(set, "sc")
	sc.SetRunAs(65534, 65534)
	sc.SetPredicates(cond("exec-condition-scripted"))
	bg := NewBGProcessService(set, "bg")
	bg.SetRunAs(65534, 65534)
	bg.SetPredicates(cond("exec-condition-bgprocess"))

	set.queueMu.Lock()
	for _, svc := range []Service{p, sc, bg} {
		if outcome, why := svc.Record().CheckPredicates(); outcome != PredOK {
			t.Errorf("%s: exec-condition failed: %s", svc.Name(), why)
		}
	}
	set.queueMu.Unlock()
	for _, n := range []string{"exec-condition-process", "exec-condition-scripted", "exec-condition-bgprocess"} {
		wantRunAsUID(t, dir, n)
	}
}
