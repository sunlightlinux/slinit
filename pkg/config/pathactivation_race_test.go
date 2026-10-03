package config

import (
	"sync"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// A path-activated service whose trigger condition already holds fires at
// ARM time — that is, from inside OnServiceLoaded, while a load is still
// in progress. pathwatch.arm() stats the path (or reads the directory) and
// calls fire() synchronously when the condition is already true, which is
// what start-on-directory-not-empty on a non-empty directory does on every
// boot. main.go's callback then starts the service.
//
// The loader fires a dependency's hook before the parent's load finishes
// ("Recursive dependency loads each fire their own notification before
// this caller's", loader.go). So the started service's propagation walks
// the graph while the parent's loadDependencies is still appending edges
// to it.
//
// That is the shape behind the path-activation stall in functional cases
// 72 and 181: two "no result received" hangs in CI plus one twelve-minute
// wedge locally, never reproducible on demand. 181 configures exactly this
// — pdirne-svc's directory is non-empty at boot, so the trigger fires
// during the load of `boot`, which waits-for three more services.
//
// This test runs that overlap deliberately. The load now happens under the
// graph lock, so the start blocks until the load is done; with the lock
// removed, -race reports the appends.

type paLogger struct{}

func (l *paLogger) ServiceStarted(name string)               {}
func (l *paLogger) ServiceStopped(name string)               {}
func (l *paLogger) ServiceFailed(name string, dep bool)      {}
func (l *paLogger) Error(format string, args ...interface{}) {}
func (l *paLogger) Info(format string, args ...interface{})  {}

func TestArmTimeFireDuringLoadIsSerialized(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&paLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	// `boot` pulls in four services, one of which is "path-activated".
	writeServiceFile(t, dir, "boot", "type = internal\n"+
		"waits-for: pa-svc\nwaits-for: other-a\nwaits-for: other-b\nwaits-for: other-c\n")
	writeServiceFile(t, dir, "pa-svc", "type = internal\nmanual = yes\n")
	for _, n := range []string{"other-a", "other-b", "other-c"} {
		writeServiceFile(t, dir, n, "type = internal\nmanual = yes\n")
	}

	var started sync.WaitGroup
	ss.OnServiceLoaded = func(svc service.Service) {
		if svc.Name() != "pa-svc" {
			return
		}
		// Exactly what main.go's path callback does when pathwatch fires
		// at arm time: hand the start to a goroutine, because
		// StartService takes the same lock that the state machine and
		// (now) the loader hold.
		started.Add(1)
		go func() {
			defer started.Done()
			ss.StartService(svc)
		}()
		// Give that goroutine a chance to be running while the rest of
		// this load appends edges, which is the whole point.
		time.Sleep(2 * time.Millisecond)
	}

	// The load itself goes under the graph lock, as the control and boot
	// paths do since the dep-graph locking change.
	var svc service.Service
	var err error
	ss.WithGraphLock(func() {
		svc, err = loader.LoadService("boot")
	})
	if err != nil {
		t.Fatalf("load boot: %v", err)
	}
	started.Wait()

	// The graph has to be intact: four edges out of boot, and each target
	// naming boot back. A lost append shows up here even without -race.
	deps := ss.DependenciesOf(svc)
	if len(deps) != 4 {
		t.Errorf("boot has %d dependencies, want 4 — an append was lost", len(deps))
	}
	for _, name := range []string{"pa-svc", "other-a", "other-b", "other-c"} {
		target := ss.FindService(name, false)
		if target == nil {
			t.Fatalf("%s not in the set", name)
		}
		found := false
		for _, dept := range ss.DependentsOf(target) {
			if dept.From == svc {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not list boot as a dependent — its edge was lost", name)
		}
	}
}
