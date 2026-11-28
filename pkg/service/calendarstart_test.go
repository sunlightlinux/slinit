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
