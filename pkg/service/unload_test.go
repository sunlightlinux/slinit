package service

import (
	"testing"
)

func TestUnloadStoppedService(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "my-svc")
	set.AddService(svc)

	// Service is STOPPED by default
	if svc.State() != StateStopped {
		t.Fatalf("expected STOPPED, got %v", svc.State())
	}

	set.UnloadService(svc)

	// Should no longer be findable
	if set.FindService("my-svc", false) != nil {
		t.Error("service should not be found after unload")
	}
}

func TestUnloadRunningServiceCheck(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "running-svc")
	set.AddService(svc)
	set.StartService(svc)

	if svc.State() != StateStarted {
		t.Fatalf("expected STARTED, got %v", svc.State())
	}

	// HasLoneRef should still be true (no dependents), but caller
	// should check State() == StateStopped before calling UnloadService.
	// This test verifies the state check pattern.
	if svc.State() == StateStopped {
		t.Error("should not be STOPPED")
	}
}

func TestUnloadWithDependentsFails(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	set.AddService(dep)

	main := NewInternalService(set, "main-svc")
	set.AddService(main)
	main.Record().AddDep(dep, DepRegular)

	// dep has a non-ordering dependent (main), so HasLoneRef should fail
	if dep.Record().HasLoneRef(0) {
		t.Error("should not have lone ref with active regular dependent")
	}
}

func TestUnloadWithOrderingDependentAllowed(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "dep-svc")
	set.AddService(dep)

	main := NewInternalService(set, "main-svc")
	set.AddService(main)
	main.Record().AddDep(dep, DepAfter)

	// dep has only ordering dependent, so HasLoneRef should succeed
	if !dep.Record().HasLoneRef(0) {
		t.Error("should have lone ref with only ordering dependent")
	}
}

func TestUnloadCleansUpDeps(t *testing.T) {
	set, _ := newTestSet()

	depA := NewInternalService(set, "dep-a")
	set.AddService(depA)

	depB := NewInternalService(set, "dep-b")
	set.AddService(depB)

	svc := NewInternalService(set, "svc")
	set.AddService(svc)
	svc.Record().AddDep(depA, DepAfter)
	svc.Record().AddDep(depB, DepBefore)

	// Verify deps are set up
	if len(svc.Dependencies()) != 2 {
		t.Fatalf("expected 2 dependencies, got %d", len(svc.Dependencies()))
	}
	if len(depA.Dependents()) != 1 {
		t.Fatalf("expected 1 dependent on depA, got %d", len(depA.Dependents()))
	}

	// Unload svc
	set.UnloadService(svc)

	// svc's deps should be cleared
	if len(svc.Dependencies()) != 0 {
		t.Errorf("expected 0 dependencies after unload, got %d", len(svc.Dependencies()))
	}

	// depA should have no dependents anymore
	if len(depA.Dependents()) != 0 {
		t.Errorf("expected 0 dependents on depA after unload, got %d", len(depA.Dependents()))
	}
	if len(depB.Dependents()) != 0 {
		t.Errorf("expected 0 dependents on depB after unload, got %d", len(depB.Dependents()))
	}

	// svc gone from set
	if set.FindService("svc", false) != nil {
		t.Error("service should not be found after unload")
	}
}

func TestUnloadClearsAlias(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "real-name")
	svc.Record().SetProvides("alias-name")
	set.AddService(svc)

	// Alias should work
	if set.FindService("alias-name", false) == nil {
		t.Fatal("alias should work before unload")
	}

	set.UnloadService(svc)

	// Both name and alias gone
	if set.FindService("real-name", false) != nil {
		t.Error("primary name should not be found after unload")
	}
	if set.FindService("alias-name", false) != nil {
		t.Error("alias should not be found after unload")
	}
}
