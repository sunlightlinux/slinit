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
