package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// testReloadLogger implements service.ServiceLogger for reload tests.
type testReloadLogger struct{}

func (l *testReloadLogger) ServiceStarted(name string)              {}
func (l *testReloadLogger) ServiceStopped(name string)              {}
func (l *testReloadLogger) ServiceFailed(name string, dep bool)     {}
func (l *testReloadLogger) Error(format string, args ...interface{}) {}
func (l *testReloadLogger) Info(format string, args ...interface{})  {}

func writeServiceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write service file: %v", err)
	}
}

func TestReloadStoppedSameType(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create initial service
	writeServiceFile(t, dir, "test-svc", "type = process\ncommand = /bin/old\n")
	svc, err := loader.LoadService("test-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	if svc.Type() != service.TypeProcess {
		t.Fatalf("expected TypeProcess, got %v", svc.Type())
	}

	// Modify the service file (same type, different command)
	writeServiceFile(t, dir, "test-svc", "type = process\ncommand = /bin/new --flag\nstop-timeout = 10\n")

	// Reload
	newSvc, err := loader.ReloadService(svc)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	// Should be same service (in-place update)
	if newSvc != svc {
		t.Fatal("expected in-place update (same pointer)")
	}

	// Verify it's still in the set
	found := ss.FindService("test-svc", false)
	if found != svc {
		t.Fatal("service not found in set after reload")
	}
}

func TestReloadStoppedTypeChange(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create initial service as internal
	writeServiceFile(t, dir, "test-svc", "type = internal\n")
	svc, err := loader.LoadService("test-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if svc.Type() != service.TypeInternal {
		t.Fatalf("expected TypeInternal, got %v", svc.Type())
	}

	// Create a dependent service
	writeServiceFile(t, dir, "dependent", "type = internal\ndepends-on:test-svc\n")
	depSvc, err := loader.LoadService("dependent")
	if err != nil {
		t.Fatalf("load dependent failed: %v", err)
	}

	// Verify dependent points to old service
	if len(depSvc.Record().Dependencies()) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(depSvc.Record().Dependencies()))
	}
	if depSvc.Record().Dependencies()[0].To != svc {
		t.Fatal("dependent should point to old service")
	}

	// Change type to process
	writeServiceFile(t, dir, "test-svc", "type = process\ncommand = /bin/test\n")

	newSvc, err := loader.ReloadService(svc)
	if err != nil {
		t.Fatalf("reload with type change failed: %v", err)
	}

	// Should be different service (new record)
	if newSvc == svc {
		t.Fatal("expected new service record for type change")
	}
	if newSvc.Type() != service.TypeProcess {
		t.Fatalf("expected TypeProcess, got %v", newSvc.Type())
	}

	// Dependent should now point to new service
	if depSvc.Record().Dependencies()[0].To != newSvc {
		t.Fatal("dependent should point to new service after type change")
	}

	// New service should be in the set
	found := ss.FindService("test-svc", false)
	if found != newSvc {
		t.Fatal("new service not found in set")
	}
}

func TestReloadStartedAllowedChanges(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create and start an internal service
	writeServiceFile(t, dir, "test-svc", "type = internal\n")
	svc, err := loader.LoadService("test-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Start the service
	svc.Start()
	ss.ProcessQueues()
	if svc.State() != service.StateStarted {
		t.Fatalf("expected STARTED, got %d", svc.State())
	}

	// Reload with same type - should succeed
	writeServiceFile(t, dir, "test-svc", "type = internal\nrestart = true\n")
	newSvc, err := loader.ReloadService(svc)
	if err != nil {
		t.Fatalf("reload started service failed: %v", err)
	}

	if newSvc != svc {
		t.Fatal("expected in-place update")
	}
}

func TestReloadStartedTypeChangeRejected(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create and start an internal service
	writeServiceFile(t, dir, "test-svc", "type = internal\n")
	svc, err := loader.LoadService("test-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	svc.Start()
	ss.ProcessQueues()

	// Try to change type while running - should fail
	writeServiceFile(t, dir, "test-svc", "type = process\ncommand = /bin/test\n")
	_, err = loader.ReloadService(svc)
	if err == nil {
		t.Fatal("expected error for type change on running service")
	}
}

func TestReloadStartedConsoleChangeRejected(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	writeServiceFile(t, dir, "test-svc", "type = internal\n")
	svc, err := loader.LoadService("test-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	svc.Start()
	ss.ProcessQueues()

	// Try to change console flags while running
	writeServiceFile(t, dir, "test-svc", "type = internal\nstarts-on-console = true\n")
	_, err = loader.ReloadService(svc)
	if err == nil {
		t.Fatal("expected error for console flag change on running service")
	}
}

func TestReloadCyclicDependencyRejected(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create A → B → C chain
	writeServiceFile(t, dir, "svc-a", "type = internal\n")
	writeServiceFile(t, dir, "svc-b", "type = internal\ndepends-on:svc-a\n")
	writeServiceFile(t, dir, "svc-c", "type = internal\ndepends-on:svc-b\n")

	_, err := loader.LoadService("svc-c")
	if err != nil {
		t.Fatalf("load chain failed: %v", err)
	}

	svcA := ss.FindService("svc-a", false)
	if svcA == nil {
		t.Fatal("svc-a not found")
	}

	// Try to make A depend on C (creating A → B → C → A cycle)
	writeServiceFile(t, dir, "svc-a", "type = internal\ndepends-on:svc-c\n")
	_, err = loader.ReloadService(svcA)
	if err == nil {
		t.Fatal("expected error for cyclic dependency")
	}
}

func TestReloadDependencyUpdate(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// Create services
	writeServiceFile(t, dir, "dep-a", "type = internal\n")
	writeServiceFile(t, dir, "dep-b", "type = internal\n")
	writeServiceFile(t, dir, "main-svc", "type = internal\ndepends-on:dep-a\n")

	_, err := loader.LoadService("main-svc")
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	mainSvc := ss.FindService("main-svc", false)
	if len(mainSvc.Record().Dependencies()) != 1 {
		t.Fatalf("expected 1 dep, got %d", len(mainSvc.Record().Dependencies()))
	}

	// Change dependency from dep-a to dep-b
	writeServiceFile(t, dir, "main-svc", "type = internal\ndepends-on:dep-b\n")
	_, err = loader.ReloadService(mainSvc)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}

	// Should now depend on dep-b
	deps := mainSvc.Record().Dependencies()
	if len(deps) != 1 {
		t.Fatalf("expected 1 dep after reload, got %d", len(deps))
	}
	if deps[0].To.Name() != "dep-b" {
		t.Fatalf("expected dep to dep-b, got %s", deps[0].To.Name())
	}
}
