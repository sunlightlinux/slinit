package main

import (
	"os"
	"regexp"
	"testing"
)

// The runner must pin its main goroutine to one OS thread before it
// does anything: seccomp filters, no-new-privs, capabilities, securebits
// and setgroups are per thread, and execve keeps only the calling
// thread's. Without the lock, 3 runs in about 1700 under GOGC=1
// GOMAXPROCS=16 started their service with NoNewPrivs: 0 although
// --no-new-privs was asked for; with it, 0 in 4000. Reproducing the
// race in a test would take thousands of runs per failure, so this
// guards the line that prevents it.
func TestMainGoroutineIsLockedToItsThread(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^func init\(\) \{ runtime\.LockOSThread\(\) \}`).Match(src) {
		t.Fatal("main.go must lock the main goroutine to its OS thread in init(): " +
			"without it a service can start without the seccomp filter, no-new-privs or " +
			"capability limits the runner applied on another thread")
	}
}
