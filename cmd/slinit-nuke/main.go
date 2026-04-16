// slinit-nuke — emergency kill-all utility for slinit-managed systems.
//
// Sends SIGTERM to every process reachable via kill(-1, sig), waits a
// short grace period, then sends SIGKILL. Intended for recovery
// scenarios where the normal shutdown path is unavailable (e.g. init
// has hung) and an operator needs to clear userspace before re-execing
// or manually rebooting.
//
// This is a deliberate sledgehammer. It does not unmount filesystems,
// sync, or coordinate with slinit — use `slinitctl shutdown` for
// orderly shutdowns.
//
// Usage:
//
//	slinit-nuke [--grace DURATION] [-9|--kill-only]
//
// With -9 the grace phase is skipped and SIGKILL is sent immediately.
package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"
)

// killFunc is swapped out in tests so they can exercise the dispatch
// logic without actually signalling the whole process tree.
var killFunc = syscall.Kill

// sleepFunc is swapped out in tests to avoid real sleeps.
var sleepFunc = time.Sleep

// run performs the nuke sequence. Returns the exit code so tests can
// assert it without calling os.Exit. In production main just passes it
// through to os.Exit.
func run(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("slinit-nuke", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var grace time.Duration
	var killOnly bool
	fs.DurationVar(&grace, "grace", 2*time.Second, "time between SIGTERM and SIGKILL (0 or -9 sends SIGKILL immediately)")
	fs.BoolVar(&killOnly, "9", false, "skip SIGTERM and send SIGKILL directly")
	fs.BoolVar(&killOnly, "kill-only", false, "skip SIGTERM and send SIGKILL directly")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Signals are best-effort; ESRCH just means "no processes matched"
	// which is a valid outcome (e.g. we are the only userspace process).
	if !killOnly {
		if err := killFunc(-1, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
			fmt.Fprintf(stderr, "slinit-nuke: kill(-1, SIGTERM): %v\n", err)
		}
		if grace > 0 {
			sleepFunc(grace)
		}
	}

	if err := killFunc(-1, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		fmt.Fprintf(stderr, "slinit-nuke: kill(-1, SIGKILL): %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}
