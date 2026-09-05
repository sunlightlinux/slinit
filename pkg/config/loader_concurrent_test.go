package config

import (
	"fmt"
	"sync"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// TestDirLoader_ConcurrentLoadService is a regression test for the
// PID-1 kernel panic surfaced by tests/performance/ssh/cases/580.
//
// Root cause: DirLoader.loading (map[string]bool) and DirLoader.curDepth
// are mutated during LoadService (lines 671/677/678 pre-fix in
// loader.go) without any mutex. Two concurrent LoadService calls
// from different control-socket connections race on this map, and
// the Go runtime terminates the process with a "fatal error:
// concurrent map read and map write". When slinit is PID 1, that
// termination triggers a kernel panic.
//
// This test spawns N goroutines that each LoadService a unique
// service name. Without the fix, Go's runtime map-race detector
// (always on for map read+write, race build not required) panics
// the test binary. With the fix, all N loads succeed.
func TestDirLoader_ConcurrentLoadService(t *testing.T) {
	const N = 32
	dir := t.TempDir()
	for i := 0; i < N; i++ {
		writeServiceFile(t, dir, fmt.Sprintf("svc-%02d", i),
			"type = scripted\ncommand = /bin/true\n")
	}

	ss := service.NewServiceSet(&testReloadLogger{})
	loader := NewDirLoader(ss, []string{dir})
	ss.SetLoader(loader)

	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("svc-%02d", idx)
			if _, err := loader.LoadService(name); err != nil {
				errCh <- fmt.Errorf("%s: %w", name, err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	if len(errs) > 0 {
		t.Fatalf("%d concurrent LoadService calls failed: %v", len(errs), errs[0])
	}

	// Sanity: every service should be present in the ServiceSet.
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("svc-%02d", i)
		if svc := ss.FindService(name, false); svc == nil {
			t.Errorf("service %s not found in ServiceSet after concurrent load", name)
		}
	}
}
