package service

import (
	"testing"
)

// testLogger is a minimal ServiceLogger for tests.
type testLogger struct {
	started []string
	stopped []string
	failed  []string
	errors  []string
}

func (l *testLogger) ServiceStarted(name string) { l.started = append(l.started, name) }
func (l *testLogger) ServiceStopped(name string) { l.stopped = append(l.stopped, name) }
func (l *testLogger) ServiceFailed(name string, _ bool) { l.failed = append(l.failed, name) }
func (l *testLogger) Error(format string, args ...interface{}) {
	l.errors = append(l.errors, format)
}
func (l *testLogger) Info(format string, args ...interface{}) {}

func newTestSet() (*ServiceSet, *testLogger) {
	logger := &testLogger{}
	set := NewServiceSet(logger)
	return set, logger
}

func TestInternalServiceStartStop(t *testing.T) {
	set, logger := newTestSet()

	svc := NewInternalService(set, "test-svc")
	set.AddService(svc)

	// Start the service
	set.StartService(svc)

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED, got %v", svc.State())
	}
	if len(logger.started) != 1 || logger.started[0] != "test-svc" {
		t.Errorf("expected ServiceStarted to be called for 'test-svc'")
	}

	// Stop the service
	set.StopService(svc)

	if svc.State() != StateStopped {
		t.Errorf("expected STOPPED, got %v", svc.State())
	}
	if len(logger.stopped) != 1 || logger.stopped[0] != "test-svc" {
		t.Errorf("expected ServiceStopped to be called for 'test-svc'")
	}
}

func TestServiceWithDependency(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	set.AddService(dep)

	main := NewInternalService(set, "main-svc")
	set.AddService(main)

	// Add dependency: main depends on dep
	main.Record().AddDep(dep, DepRegular)

	// Start main service - should also start dependency
	set.StartService(main)

	if dep.State() != StateStarted {
		t.Errorf("dependency should be STARTED, got %v", dep.State())
	}
	if main.State() != StateStarted {
		t.Errorf("main service should be STARTED, got %v", main.State())
	}

	// Stop main - dependency should also stop (since nothing else requires it)
	set.StopService(main)

	if main.State() != StateStopped {
		t.Errorf("main service should be STOPPED, got %v", main.State())
	}
	if dep.State() != StateStopped {
		t.Errorf("dependency should be STOPPED, got %v", dep.State())
	}
}

func TestServiceChainDependencies(t *testing.T) {
	set, _ := newTestSet()

	svcA := NewInternalService(set, "svc-a")
	svcB := NewInternalService(set, "svc-b")
	svcC := NewInternalService(set, "svc-c")

	set.AddService(svcA)
	set.AddService(svcB)
	set.AddService(svcC)

	// C depends on B, B depends on A
	svcC.Record().AddDep(svcB, DepRegular)
	svcB.Record().AddDep(svcA, DepRegular)

	// Start C - should start entire chain
	set.StartService(svcC)

	if svcA.State() != StateStarted {
		t.Errorf("svc-a should be STARTED, got %v", svcA.State())
	}
	if svcB.State() != StateStarted {
		t.Errorf("svc-b should be STARTED, got %v", svcB.State())
	}
	if svcC.State() != StateStarted {
		t.Errorf("svc-c should be STARTED, got %v", svcC.State())
	}

	// Stop C - entire chain should stop
	set.StopService(svcC)

	if svcA.State() != StateStopped {
		t.Errorf("svc-a should be STOPPED, got %v", svcA.State())
	}
	if svcB.State() != StateStopped {
		t.Errorf("svc-b should be STOPPED, got %v", svcB.State())
	}
	if svcC.State() != StateStopped {
		t.Errorf("svc-c should be STOPPED, got %v", svcC.State())
	}
}

func TestServiceRequiredByMultiple(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "shared-dep")
	svcA := NewInternalService(set, "svc-a")
	svcB := NewInternalService(set, "svc-b")

	set.AddService(dep)
	set.AddService(svcA)
	set.AddService(svcB)

	// Both A and B depend on dep
	svcA.Record().AddDep(dep, DepRegular)
	svcB.Record().AddDep(dep, DepRegular)

	// Start both
	set.StartService(svcA)
	set.StartService(svcB)

	if dep.State() != StateStarted {
		t.Errorf("dep should be STARTED, got %v", dep.State())
	}

	// Stop A - dep should remain started because B still needs it
	set.StopService(svcA)

	if svcA.State() != StateStopped {
		t.Errorf("svc-a should be STOPPED, got %v", svcA.State())
	}
	if dep.State() != StateStarted {
		t.Errorf("dep should still be STARTED (needed by svc-b), got %v", dep.State())
	}

	// Stop B - now dep should also stop
	set.StopService(svcB)

	if dep.State() != StateStopped {
		t.Errorf("dep should be STOPPED, got %v", dep.State())
	}
}

func TestServicePinStart(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "pinned-svc")
	set.AddService(svc)

	// Start and pin
	set.StartService(svc)
	svc.PinStart()

	if svc.State() != StateStarted {
		t.Errorf("expected STARTED, got %v", svc.State())
	}

	// Try to stop - should remain started due to pin
	svc.Stop(true)
	set.ProcessQueues()

	if svc.State() != StateStarted {
		t.Errorf("pinned service should remain STARTED, got %v", svc.State())
	}

	// Unpin - should now stop
	svc.Unpin()

	if svc.State() != StateStopped {
		t.Errorf("unpinned service should be STOPPED, got %v", svc.State())
	}
}

func TestServicePinStop(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "pin-stopped-svc")
	set.AddService(svc)

	// Pin stopped
	svc.PinStop()

	// Try to start - should fail
	svc.Start()
	set.ProcessQueues()

	if svc.State() != StateStopped {
		t.Errorf("pin-stopped service should remain STOPPED, got %v", svc.State())
	}
}

func TestStopAllServices(t *testing.T) {
	set, _ := newTestSet()

	svcA := NewInternalService(set, "svc-a")
	svcB := NewInternalService(set, "svc-b")
	svcC := NewInternalService(set, "svc-c")

	set.AddService(svcA)
	set.AddService(svcB)
	set.AddService(svcC)

	set.StartService(svcA)
	set.StartService(svcB)
	set.StartService(svcC)

	// All should be started
	if set.CountActiveServices() != 3 {
		t.Errorf("expected 3 active services, got %d", set.CountActiveServices())
	}

	// Stop all
	set.StopAllServices(ShutdownHalt)

	if svcA.State() != StateStopped {
		t.Errorf("svc-a should be STOPPED, got %v", svcA.State())
	}
	if svcB.State() != StateStopped {
		t.Errorf("svc-b should be STOPPED, got %v", svcB.State())
	}
	if svcC.State() != StateStopped {
		t.Errorf("svc-c should be STOPPED, got %v", svcC.State())
	}
	if set.CountActiveServices() != 0 {
		t.Errorf("expected 0 active services, got %d", set.CountActiveServices())
	}
}

func TestServiceRestart(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "restart-svc")
	set.AddService(svc)

	set.StartService(svc)
	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// Restart
	result := svc.Restart()
	set.ProcessQueues()

	if !result {
		t.Error("Restart() should return true for started service")
	}
	// After restart of an internal service, it should be started again
	if svc.State() != StateStarted {
		t.Errorf("expected STARTED after restart, got %v", svc.State())
	}
}

// testListener tracks service events.
type testListener struct {
	events []ServiceEvent
}

func (l *testListener) ServiceEvent(_ Service, event ServiceEvent) {
	l.events = append(l.events, event)
}

func TestServiceListenerNotifications(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "listener-svc")
	set.AddService(svc)

	listener := &testListener{}
	svc.AddListener(listener)

	set.StartService(svc)

	if len(listener.events) != 1 || listener.events[0] != EventStarted {
		t.Errorf("expected [STARTED] event, got %v", listener.events)
	}

	set.StopService(svc)

	if len(listener.events) != 2 || listener.events[1] != EventStopped {
		t.Errorf("expected [STARTED, STOPPED] events, got %v", listener.events)
	}
}
