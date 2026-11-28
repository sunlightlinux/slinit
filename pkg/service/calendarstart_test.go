package service

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A service with start-on-calendar is started at the times it names
// (every second here), again after it stopped, and no more once its
// timer is removed.
func TestCalendarStarter(t *testing.T) {
	set, logger := newTestSet()
	s := sleeper(set, "c")
	if err := s.Record().SetStartOnCalendar("*-*-* *:*:*"); err != nil {
		t.Fatal(err)
	}
	cs := NewCalendarStarter(set, logger)
	defer cs.Close()
	defer set.StopService(s)
	cs.Add(s)
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("%v, want started by the calendar", got)
	}
	set.StopService(s)
	waitState(t, s, StateStopped, 3*time.Second)
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("%v, want started again at the next time", got)
	}
	cs.Remove("c")
	set.StopService(s)
	waitState(t, s, StateStopped, 3*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if st := s.State(); st != StateStopped {
		t.Errorf("%v after the timer was removed, want STOPPED", st)
	}
}

// A service whose expression was cleared (a reload) is not started by a
// timer still armed for it.
func TestCalendarStarterHonoursACleared(t *testing.T) {
	set, logger := newTestSet()
	s := sleeper(set, "c")
	s.Record().SetStartOnCalendar("*-*-* *:*:*")
	cs := NewCalendarStarter(set, logger)
	defer cs.Close()
	cs.Add(s)
	set.WithGraphLock(func() { s.Record().SetStartOnCalendar("") })
	time.Sleep(1500 * time.Millisecond)
	if st := s.State(); st != StateStopped {
		t.Errorf("%v, want left STOPPED", st)
	}
}

// A scripted job stays STARTED after its command: the calendar runs it
// again at each time, rather than once.
func TestCalendarStarterRerunsAScriptedJob(t *testing.T) {
	set, logger := newTestSet()
	runs := t.TempDir() + "/runs"
	s := NewScriptedService(set, "job")
	s.SetStartCommand([]string{"/bin/sh", "-c", "echo x >> " + runs})
	set.AddService(s)
	if err := s.Record().SetStartOnCalendar("*-*-* *:*:*"); err != nil {
		t.Fatal(err)
	}
	cs := NewCalendarStarter(set, logger)
	defer cs.Close()
	defer set.StopService(s)
	cs.Add(s)
	time.Sleep(3500 * time.Millisecond)
	b, _ := os.ReadFile(runs)
	if n := strings.Count(string(b), "\n"); n < 2 {
		t.Errorf("the job ran %d times in 3.5s of a calendar firing every second", n)
	}
}

// A wall clock behind the time just fired for (stepped back while the
// timer ran) does not name that time again.
func TestNextCalendarFireAfterClockStep(t *testing.T) {
	spec, err := ParseCalendar("*-*-* 03:00:00 UTC")
	if err != nil {
		t.Fatal(err)
	}
	fired := time.Date(2026, 11, 28, 3, 0, 0, 0, time.UTC)
	now := fired.Add(-5 * time.Millisecond)
	if got := nextCalendarFire(spec, now, fired); !got.Equal(fired.Add(24 * time.Hour)) {
		t.Errorf("next %v, want the next day's", got)
	}
}

// start-on-calendar-persistent: a time missed since the last recorded
// start is caught up when the timer is armed; with no record, nothing
// is; and each start is recorded.
func TestCalendarPersistentCatchUp(t *testing.T) {
	old := cronPersistDir
	cronPersistDir = t.TempDir()
	defer func() { cronPersistDir = old }()
	set, logger := newTestSet()
	s := sleeper(set, "p")
	s.Record().SetStartOnCalendar("*-*-* 03:00:00")
	s.Record().SetStartOnCalendarPersistent(true)
	cs := NewCalendarStarter(set, logger)
	defer cs.Close()
	defer set.StopService(s)

	cs.Add(s) // no record yet: nothing to catch up
	time.Sleep(200 * time.Millisecond)
	if st := s.State(); st != StateStopped {
		t.Fatalf("started with no record of a last start: %v", st)
	}
	writeStamp(calendarStampPath("p"), time.Now().Add(-48*time.Hour))
	cs.Add(s)
	if got := waitState(t, s, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("a missed 03:00 was not caught up: %v", got)
	}
	time.Sleep(100 * time.Millisecond)
	if last, ok := readStamp(calendarStampPath("p")); !ok || time.Since(last) > time.Minute {
		t.Errorf("the catch-up start was not recorded: %v %v", last, ok)
	}
}

// Running the job again is not a restart: it is not counted as one;
// and one a running service depends on is not run again (its stop
// would wait for that service or take it down), the service keeping
// its process.
func TestCalendarRerunLeavesDependentsAlone(t *testing.T) {
	set, _ := newTestSet()
	runs := t.TempDir() + "/runs"
	job := NewScriptedService(set, "job")
	job.SetStartCommand([]string{"/bin/sh", "-c", "echo x >> " + runs})
	set.AddService(job)
	if err := job.Record().SetStartOnCalendar("*-*-* 03:00:00"); err != nil {
		t.Fatal(err)
	}
	dep := sleeper(set, "dep")
	dep.Record().AddDep(job, DepRegular) // before either runs (-race)
	set.StartService(job)                // explicitly: it stays up when dep stops
	waitState(t, job, StateStarted, 3*time.Second)
	set.StartService(dep)
	defer set.StopService(dep)
	defer set.StopService(job)
	if got := waitState(t, dep, StateStarted, 3*time.Second); got != StateStarted {
		t.Fatalf("dep %v", got)
	}
	pid := dep.PID()
	if set.calendarFire(job) {
		t.Error("calendarFire ran a job a running service depends on")
	}
	time.Sleep(300 * time.Millisecond)
	if dep.PID() != pid || dep.State() != StateStarted || job.State() != StateStarted {
		t.Errorf("dep went from pid %d to %d (%v), job %v", pid, dep.PID(), dep.State(), job.State())
	}
	// With the dependent stopped it runs again, counted as no restart.
	set.StopService(dep)
	waitState(t, dep, StateStopped, 3*time.Second)
	if st := job.State(); st != StateStarted {
		t.Fatalf("job %v once dep stopped, want STARTED (the rerun path)", st)
	}
	if !set.calendarFire(job) {
		t.Fatal("calendarFire ran nothing")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(runs)
		if strings.Count(string(b), "\n") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job did not run again: %q", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitState(t, job, StateStarted, 3*time.Second)
	if n := job.Record().RestartCount(); n != 0 {
		t.Errorf("the rerun was counted as %d restarts", n)
	}
}

// start-on-calendar-randomized-delay: each start is up to the delay
// later than the time named.
func TestCalendarRandomizedDelay(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if j := calendarJitter(time.Second); j < 0 || j >= time.Second {
			t.Fatalf("jitter %v out of [0, 1s)", j)
		}
	}
	if calendarJitter(0) != 0 {
		t.Fatal("jitter with no delay")
	}
	old := calendarJitter
	defer func() { calendarJitter = old }()
	calendarJitter = func(d time.Duration) time.Duration {
		if d != 1500*time.Millisecond {
			t.Errorf("jitter asked for %v, want the configured 1.5s", d)
		}
		return d
	}
	set, logger := newTestSet()
	runs := t.TempDir() + "/runs"
	s := NewScriptedService(set, "late")
	s.SetStartCommand([]string{"/bin/sh", "-c", "echo x >> " + runs})
	set.AddService(s)
	if err := s.Record().SetStartOnCalendar("*-*-* *:*:*"); err != nil {
		t.Fatal(err)
	}
	s.Record().SetStartOnCalendarRandomizedDelay(1500 * time.Millisecond)
	cs := NewCalendarStarter(set, logger)
	defer cs.Close()
	defer set.StopService(s)
	cs.Add(s)
	time.Sleep(1200 * time.Millisecond)
	if b, _ := os.ReadFile(runs); len(b) != 0 {
		t.Fatalf("ran within 1.2s with every start 1.5s late: %q", b)
	}
	time.Sleep(1500 * time.Millisecond)
	if b, _ := os.ReadFile(runs); len(b) == 0 {
		t.Error("did not run by 2.7s: a time 1.5s late is at most 2.5s away")
	}
}

// A re-arm (a reload) inside the randomized delay, after the time named
// and before the late start, runs the job at once instead of losing it.
func TestCalendarRearmInsideDelayKeepsTheRun(t *testing.T) {
	old := calendarJitter
	defer func() { calendarJitter = old }()
	calendarJitter = func(d time.Duration) time.Duration { return d } // always the whole delay
	set, logger := newTestSet()
	runs := t.TempDir() + "/runs"
	s := NewScriptedService(set, "rearm")
	s.SetStartCommand([]string{"/bin/sh", "-c", "echo x >> " + runs})
	set.AddService(s)
	if err := s.Record().SetStartOnCalendar("*-*-* *:*:*"); err != nil {
		t.Fatal(err)
	}
	s.Record().SetStartOnCalendarRandomizedDelay(time.Hour)
	cs := NewCalendarStarter(set, logger)
	defer cs.Close()
	defer set.StopService(s)
	cs.Add(s)
	time.Sleep(1100 * time.Millisecond) // the time named has gone by; its start is an hour off
	// A reload: OnServiceUnloaded's Remove, then OnServiceLoaded's Add.
	cs.Remove("rearm")
	cs.Add(s)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if b, _ := os.ReadFile(runs); len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run caught by the re-arm was lost")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A service a condition skipped is started over at the next fire, its
// conditions checked again, as systemd does at every elapse; a skipped
// job's stop runs no stop-command; one marked down is not started.
func TestTimerFireSkippedAndDown(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	gate := dir + "/gate"
	s := sleeper(set, "gated")
	s.Record().SetPredicates([]Predicate{{Kind: PredPathExists, Param: gate}})
	if err := s.Record().SetStartOnCalendar("*-*-* 03:00:00"); err != nil {
		t.Fatal(err)
	}
	set.StartService(s)
	defer set.StopService(s)
	waitState(t, s, StateStarted, 3*time.Second)
	if s.PID() > 0 {
		t.Fatal("ran with its condition false")
	}
	os.WriteFile(gate, nil, 0o644)
	if !set.calendarFire(s) {
		t.Fatal("the fire did nothing for a skipped service")
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.PID() <= 0 {
		if time.Now().After(deadline) {
			t.Fatal("not started over with its condition now true")
		}
		time.Sleep(10 * time.Millisecond)
	}

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

	d := sleeper(set, "down")
	if err := d.Record().SetStartOnCalendar("*-*-* 03:00:00"); err != nil {
		t.Fatal(err)
	}
	d.Record().SetMarkedDown(true)
	defer set.StopService(d)
	if set.calendarFire(d) {
		t.Error("a service marked down was started by its timer")
	}
}
