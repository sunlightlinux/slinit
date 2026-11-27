package main

import (
	"runtime/debug"
	"testing"
)

// TestThreadLimitAllowsManyChildren: every running child holds one OS
// thread in a blocking wait, and the Go runtime kills a process that
// goes past its thread limit. The default of 10000 let roughly 9900
// running services take PID 1 down; the harness that found it started
// 10500 `sleep` services and slinit died with "fatal error: thread
// exhaustion" before the boot finished.
func TestThreadLimitAllowsManyChildren(t *testing.T) {
	prev := debug.SetMaxThreads(1 << 30) // read the current limit
	debug.SetMaxThreads(prev)
	if prev < 1<<20 {
		t.Fatalf("Go thread limit is %d: a system running that many services kills PID 1", prev)
	}
}
