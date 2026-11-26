package config

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/mountwatch"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// A reload has to move a path or mount watch to the new configuration.
// OnServiceLoaded ran only on a fresh load, so a reload that changed
// start-on-path-exists kept watching the old path, the unload that
// followed removed the NEW path (leaking the old watch, so loading the
// service again failed "already registered"), and after a type change the
// watch's callback still held the replaced record, no longer in the set.
// A reload now reports the old record and configuration as unloaded and
// the new ones as loaded; a type change that keeps the watches reports the
// record replaced.
func TestReloadRunsLoadHooks(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	var events []string
	// watched mimics main.go's pathwatch registry: one record per path.
	watched := map[string]service.Service{}
	ss.OnServiceLoaded = func(svc service.Service) {
		path, _ := svc.Record().StartOnPath()
		events = append(events, fmt.Sprintf("load %s %p", path, svc))
		if _, dup := watched[path]; dup {
			t.Errorf("path %s already registered", path)
		}
		watched[path] = svc
	}
	ss.OnServiceUnloaded = func(svc service.Service) {
		path, _ := svc.Record().StartOnPath()
		events = append(events, fmt.Sprintf("unload %s %p", path, svc))
		delete(watched, path)
	}
	ss.OnServiceReplaced = func(oldSvc, newSvc service.Service) {
		path, _ := newSvc.Record().StartOnPath()
		events = append(events, fmt.Sprintf("replace %s %p %p", path, oldSvc, newSvc))
		if watched[path] != oldSvc {
			t.Errorf("replaced record does not own the watch on %s", path)
		}
		watched[path] = newSvc
	}

	writeServiceFile(t, dir, "pa", "type = internal\nstart-on-path-exists = /run/a\n")
	svc1, err := loader.LoadService("pa")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Same type: updated in place.
	writeServiceFile(t, dir, "pa", "type = internal\nstart-on-path-exists = /run/b\n")
	if got, err := loader.ReloadService(svc1); err != nil || got != svc1 {
		t.Fatalf("reload in place: %v (same record: %v)", err, got == svc1)
	}

	// Type change: a new record replaces the old.
	writeServiceFile(t, dir, "pa", "type = scripted\ncommand = /bin/true\nstart-on-path-exists = /run/b\n")
	svc2, err := loader.ReloadService(svc1)
	if err != nil {
		t.Fatalf("reload with type change: %v", err)
	}
	if svc2 == svc1 || ss.FindService("pa", false) != svc2 {
		t.Fatalf("type-change reload did not install a new record")
	}

	ss.UnloadService(svc2)

	want := []string{
		fmt.Sprintf("load /run/a %p", svc1),
		fmt.Sprintf("unload /run/a %p", svc1),
		fmt.Sprintf("load /run/b %p", svc1),
		fmt.Sprintf("replace /run/b %p %p", svc1, svc2),
		fmt.Sprintf("unload /run/b %p", svc2),
	}
	if fmt.Sprint(events) != fmt.Sprint(want) {
		t.Errorf("hooks saw\n  %q\nwant\n  %q", events, want)
	}
	if len(watched) != 0 {
		t.Errorf("watches left after unload: %v", watched)
	}
}

// A reload that fails leaves the old watch in place: the unloaded hook
// must not run for a configuration that is kept.
func TestReloadFailureKeepsLoadHooks(t *testing.T) {
	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	var events []string
	ss.OnServiceLoaded = func(svc service.Service) {
		path, _ := svc.Record().StartOnPath()
		events = append(events, "load "+path)
	}
	ss.OnServiceUnloaded = func(svc service.Service) {
		path, _ := svc.Record().StartOnPath()
		events = append(events, "unload "+path)
	}

	writeServiceFile(t, dir, "pa", "type = internal\nstart-on-path-exists = /run/a\n")
	svc, err := loader.LoadService("pa")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	writeServiceFile(t, dir, "pa", "type = internal\nstart-on-path-exists = /run/b\ndepends-on: missing-dep\n")
	if _, err := loader.ReloadService(svc); err == nil {
		t.Fatal("reload with a missing dependency succeeded")
	}
	if fmt.Sprint(events) != fmt.Sprint([]string{"load /run/a"}) {
		t.Errorf("hooks saw %q, want only the first load", events)
	}
	if p, _ := svc.Record().StartOnPath(); p != "/run/a" {
		t.Errorf("failed reload changed the path to %q", p)
	}
}

type nopMountLogger struct{}

func (nopMountLogger) Info(string, ...interface{})  {}
func (nopMountLogger) Warn(string, ...interface{})  {}
func (nopMountLogger) Debug(string, ...interface{}) {}

// A reload that leaves start-on-mount alone must not re-arm the watch. The
// hooks re-added it on every reload, and mountwatch fires on Add for a path
// already mounted, so `slinitctl stop svc` followed by a reload that
// changed nothing (or reload-all) started the service again. Hooks wired
// as main.go wires them, on a real watcher and a path that is mounted.
func TestReloadDoesNotRefireMountActivation(t *testing.T) {
	mw, err := mountwatch.New(nopMountLogger{})
	if err != nil {
		t.Skipf("mountwatch: %v", err)
	}
	defer mw.Close()

	dir := t.TempDir()
	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	var fires atomic.Int32
	ss.OnServiceLoaded = func(svc service.Service) {
		if mp := svc.Record().StartOnMount(); mp != "" {
			if err := mw.Add(mp, svc.Name(), func() { fires.Add(1) }); err != nil {
				t.Errorf("add: %v", err)
			}
		}
	}
	ss.OnServiceUnloaded = func(svc service.Service) {
		if mp := svc.Record().StartOnMount(); mp != "" {
			mw.Remove(mp, svc.Name())
		}
	}

	writeServiceFile(t, dir, "ma", "type = internal\nstart-on-mount = /\n")
	svc, err := loader.LoadService("ma")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if n := fires.Load(); n != 1 {
		t.Fatalf("load fired %d times, want 1 (/ is mounted)", n)
	}

	// Unchanged: updated in place.
	if svc, err = loader.ReloadService(svc); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if n := fires.Load(); n != 1 {
		t.Errorf("unchanged reload fired the mount activation again (%d fires)", n)
	}

	// Type change, same mount: the record is replaced.
	writeServiceFile(t, dir, "ma", "type = scripted\ncommand = /bin/true\nstart-on-mount = /\n")
	if _, err = loader.ReloadService(svc); err != nil {
		t.Fatalf("reload with type change: %v", err)
	}
	if n := fires.Load(); n != 1 {
		t.Errorf("type-change reload fired the mount activation again (%d fires)", n)
	}
}
