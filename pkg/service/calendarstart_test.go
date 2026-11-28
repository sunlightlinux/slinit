package service

import (
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
