package service

import (
	"testing"
)

func TestFindServiceByAlias(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "real-name")
	svc.Record().SetProvides("alias-name")
	set.AddService(svc)

	// Find by primary name
	found := set.FindService("real-name", false)
	if found == nil {
		t.Fatal("should find service by primary name")
	}

	// Find by alias
	found = set.FindService("alias-name", false)
	if found == nil {
		t.Fatal("should find service by alias")
	}
	if found.Name() != "real-name" {
		t.Errorf("expected service name 'real-name', got '%s'", found.Name())
	}
}

func TestAliasNotFound(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "svc")
	set.AddService(svc)

	found := set.FindService("nonexistent-alias", false)
	if found != nil {
		t.Error("should not find service by nonexistent alias")
	}
}

func TestRemoveServiceClearsAlias(t *testing.T) {
	set, _ := newTestSet()

	svc := NewInternalService(set, "svc")
	svc.Record().SetProvides("my-alias")
	set.AddService(svc)

	// Alias works
	if set.FindService("my-alias", false) == nil {
		t.Fatal("alias should work before removal")
	}

	// Remove service
	set.RemoveService(svc)

	// Alias should be gone
	if set.FindService("my-alias", false) != nil {
		t.Error("alias should be removed after RemoveService")
	}
}

func TestReplaceServiceUpdatesAlias(t *testing.T) {
	set, _ := newTestSet()

	oldSvc := NewInternalService(set, "svc")
	oldSvc.Record().SetProvides("old-alias")
	set.AddService(oldSvc)

	newSvc := NewInternalService(set, "svc")
	newSvc.Record().SetProvides("new-alias")
	set.ReplaceService(oldSvc, newSvc)

	// Old alias should be gone
	if set.FindService("old-alias", false) != nil {
		t.Error("old alias should be removed after ReplaceService")
	}

	// New alias should work
	found := set.FindService("new-alias", false)
	if found == nil {
		t.Fatal("new alias should work after ReplaceService")
	}
}

func TestDependencyViaAlias(t *testing.T) {
	set, _ := newTestSet()

	dep := NewInternalService(set, "real-dep")
	dep.Record().SetProvides("dep-alias")
	set.AddService(dep)

	svc := NewInternalService(set, "main-svc")
	set.AddService(svc)

	// Add dependency using the alias name (resolved by FindService)
	depFound := set.FindService("dep-alias", false)
	if depFound == nil {
		t.Fatal("should find dep by alias")
	}
	svc.Record().AddDep(depFound, DepRegular)

	// Start main service - should also start dependency
	set.StartService(svc)

	if dep.State() != StateStarted {
		t.Errorf("dependency should be STARTED, got %v", dep.State())
	}
	if svc.State() != StateStarted {
		t.Errorf("main service should be STARTED, got %v", svc.State())
	}
}
