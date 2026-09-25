package service

import (
	"fmt"
	"sync"
	"testing"
)

// TestServiceRecord_ConcurrentEnvMutation reproduces the PID-1 kill found by
// perf case 470-concurrent-setenv-8 on ceres (slinit v2.4.2).
//
// Each control connection is served on its own goroutine (control/server.go
// spawns one per accept), and CmdSetEnv reaches extraEnv through
// SetEnvVar/UnsetEnvVar without going through the ServiceSet mutex. Eight
// concurrent `slinitctl setenv` on one service therefore wrote the same bare
// map at once, which is a Go runtime fatal error — not a recoverable panic.
// In PID 1 that is a kernel panic.
//
// Run with -race. Without the fix this fails as either a race report or a
// hard "concurrent map writes" abort of the test binary.
func TestServiceRecord_ConcurrentEnvMutation(t *testing.T) {
	sr := &ServiceRecord{serviceName: "envrace"}

	const workers = 8
	const iterations = 200

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			key := fmt.Sprintf("PERF%d", w)
			for i := 0; i < iterations; i++ {
				sr.SetEnvVar(key, "val")
				sr.UnsetEnvVar(key)
			}
		}(w)
	}

	// Readers race the writers the same way `slinitctl getallenv` and a
	// service start (BuildEnvSlice) do.
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				_ = sr.GetAllEnv()
				_ = sr.BuildEnvSlice()
			}
		}()
	}

	wg.Wait()
}

// TestServiceRecord_ConcurrentResetEnv covers the other mutator: ResetEnv
// nils the map out from under a concurrent writer, which is the same fatal
// error by a different route (`slinitctl reset-env` during a setenv burst).
func TestServiceRecord_ConcurrentResetEnv(t *testing.T) {
	sr := &ServiceRecord{serviceName: "envreset"}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sr.SetEnvVar("KEY", "val")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			sr.ResetEnv()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = sr.GetAllEnv()
		}
	}()
	wg.Wait()
}
