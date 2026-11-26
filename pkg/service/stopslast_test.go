package service

import (
	"strings"
	"testing"
)

type stopRecorder struct{ order *[]string }

func (r stopRecorder) ServiceEvent(svc Service, ev ServiceEvent) {
	if ev == EventStopped {
		*r.order = append(*r.order, svc.Name())
	}
}

// A stops-last service — the log daemon — and the filesystems it
// depends on keep running through a shutdown until every other service
// has stopped, so what the others log on their way down is persisted.
// Without it the log daemon, depended on by nothing, stopped among the
// first.
func TestStopsLastStopsAfterEverythingElse(t *testing.T) {
	set, _ := newTestSet()
	names := []string{"boot", "filesystems", "journald", "app", "web"}
	svcs := map[string]*InternalService{}
	var order []string
	for _, n := range names {
		s := NewInternalService(set, n)
		svcs[n] = s
		set.AddService(s)
		s.AddListener(stopRecorder{&order})
	}
	svcs["journald"].Record().Flags.StopsLast = true
	svcs["journald"].Record().AddDep(svcs["filesystems"], DepRegular)
	svcs["app"].Record().AddDep(svcs["filesystems"], DepRegular)
	svcs["web"].Record().AddDep(svcs["app"], DepRegular)
	for _, n := range []string{"journald", "app", "web"} {
		svcs["boot"].Record().AddDep(svcs[n], DepWaitsFor)
	}

	set.StartService(svcs["boot"])
	for _, n := range names {
		if svcs[n].State() != StateStarted {
			t.Fatalf("%s not started", n)
		}
	}
	set.StopAllServices(ShutdownHalt)

	for _, n := range names {
		if svcs[n].State() != StateStopped {
			t.Errorf("%s still %v after shutdown", n, svcs[n].State())
		}
	}
	got := strings.Join(order, ",")
	if !strings.HasSuffix(got, "journald,filesystems") {
		t.Errorf("stop order %s: want journald then filesystems last", got)
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("%d services still counted active", set.CountActiveServices())
	}
}

// releaseStopsLastLocked checks the service its last scan stopped at
// before scanning again. The answer must stay the scan's: blocked while
// any other service runs, even once the remembered one has stopped, has
// left the set or become dependency-pinned; released once all have
// stopped.
func TestReleaseStopsLastRemembersItsBlocker(t *testing.T) {
	set, _ := newTestSet()
	last := NewInternalService(set, "last")
	set.AddService(last)
	a := NewInternalService(set, "a")
	set.AddService(a)
	b := NewInternalService(set, "b")
	set.AddService(b)
	last.Record().state.Store(StateStarted)
	a.Record().state.Store(StateStopping)
	b.Record().state.Store(StateStopping)

	release := func() bool {
		set.queueMu.Lock()
		defer set.queueMu.Unlock()
		set.stopsLast = []Service{last}
		return set.releaseStopsLastLocked()
	}
	// Run the scan without letting the release stop anything: the
	// stops-last service is put back each time.
	blocked := func(why string) {
		t.Helper()
		if release() {
			t.Fatalf("released while %s", why)
		}
	}

	blocked("a and b are stopping")
	first := set.stopsLastBlocker
	if first != a && first != b {
		t.Fatalf("blocker = %v, want a or b", first)
	}
	other := a
	if first == a {
		other = b
	}

	// The remembered blocker stops: the other one still blocks.
	first.Record().state.Store(StateStopped)
	blocked("one service is still stopping")
	if set.stopsLastBlocker != other {
		t.Errorf("blocker = %v, want %v", set.stopsLastBlocker, other)
	}

	// It restarts but leaves the set; the other one is now pinned by a
	// dependent. Neither may block any more.
	first.Record().state.Store(StateStarting)
	set.RemoveService(first)
	other.Record().deptPinnedStarted = true
	set.stopsLastBlocker = first
	if !release() {
		t.Fatal("not released with only a removed service and a dependency-pinned one running")
	}
	if set.stopsLastBlocker != nil {
		t.Error("blocker kept after the release")
	}
}

// Each condition the remembered blocker is checked against matters on
// its own: a blocker that has stopped, left the set, become
// dependency-pinned or become the held service no longer blocks, and
// with nothing else running the release must happen.
func TestReleaseStopsLastStaleBlocker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale func(set *ServiceSet, last, blk Service) []Service // returns stopsLast
	}{
		{"stopped", func(set *ServiceSet, last, blk Service) []Service {
			blk.Record().state.Store(StateStopped)
			return []Service{last}
		}},
		{"removed", func(set *ServiceSet, last, blk Service) []Service {
			set.RemoveService(blk)
			return []Service{last}
		}},
		{"dependency-pinned", func(set *ServiceSet, last, blk Service) []Service {
			blk.Record().deptPinnedStarted = true
			return []Service{last}
		}},
		{"held", func(set *ServiceSet, last, blk Service) []Service {
			return []Service{last, blk}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set, _ := newTestSet()
			last := NewInternalService(set, "last")
			set.AddService(last)
			blk := NewInternalService(set, "blk")
			set.AddService(blk)
			last.Record().state.Store(StateStarted)
			blk.Record().state.Store(StateStopping)

			set.queueMu.Lock()
			defer set.queueMu.Unlock()
			set.stopsLast = []Service{last}
			if set.releaseStopsLastLocked() {
				t.Fatal("released while blk is stopping")
			}
			if set.stopsLastBlocker != blk {
				t.Fatalf("blocker = %v, want blk", set.stopsLastBlocker)
			}
			set.stopsLast = tc.stale(set, last, blk)
			if !set.releaseStopsLastLocked() {
				t.Fatal("not released although nothing else blocks")
			}
		})
	}
}
