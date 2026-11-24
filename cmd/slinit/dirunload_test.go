package main

import (
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

type quietSvcLogger struct{}

func (quietSvcLogger) ServiceStarted(string)        {}
func (quietSvcLogger) ServiceStopped(string)        {}
func (quietSvcLogger) ServiceFailed(string, bool)   {}
func (quietSvcLogger) Error(string, ...interface{}) {}
func (quietSvcLogger) Info(string, ...interface{})  {}

func noClient(service.Service) bool { return false }

// Removing the file of a stopped service that another loaded service
// depends-on must not unload it: the unload strips the hard edge, and
// the dependent then starts without its dependency.
func TestUnloadDisappearedKeepsHardDependency(t *testing.T) {
	set := service.NewServiceSet(quietSvcLogger{})
	b := service.NewInternalService(set, "b")
	a := service.NewInternalService(set, "a")
	set.AddService(b)
	set.AddService(a)
	if err := set.AddDependency(a, b, service.DepRegular); err != nil {
		t.Fatal(err)
	}

	if got := unloadDisappeared(set, "b", noClient); got != dirUnloadReferenced {
		t.Errorf("outcome = %v, want dirUnloadReferenced", got)
	}
	if set.FindService("b", false) == nil {
		t.Fatal("b was unloaded while a depends-on it")
	}
	if n := len(a.Record().Dependencies()); n != 1 {
		t.Fatalf("a has %d dependencies after b's file vanished, want 1", n)
	}
	set.StartService(a)
	if b.State() != service.StateStarted {
		t.Errorf("a started without its dependency: b is %v", b.State())
	}
}

// A service a control client holds a handle to stays loaded too, as the
// unload command requires.
func TestUnloadDisappearedKeepsClientHeld(t *testing.T) {
	set := service.NewServiceSet(quietSvcLogger{})
	b := service.NewInternalService(set, "b")
	set.AddService(b)

	held := func(s service.Service) bool { return s == b }
	if got := unloadDisappeared(set, "b", held); got != dirUnloadReferenced {
		t.Errorf("outcome = %v, want dirUnloadReferenced", got)
	}
	if set.FindService("b", false) == nil {
		t.Fatal("b was unloaded while a control client holds it")
	}
}

// The plain case still unloads: stopped, unreferenced, unheld.
func TestUnloadDisappearedUnloadsLoneService(t *testing.T) {
	set := service.NewServiceSet(quietSvcLogger{})
	b := service.NewInternalService(set, "b")
	set.AddService(b)
	if got := unloadDisappeared(set, "b", noClient); got != dirUnloadDone {
		t.Errorf("outcome = %v, want dirUnloadDone", got)
	}
	if set.FindService("b", false) != nil {
		t.Error("lone stopped service was not unloaded")
	}
}
