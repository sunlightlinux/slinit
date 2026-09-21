package eventloop

import (
	"os"
	"syscall"
	"testing"
)

// TestShutdownSignalSet pins the signals claimed before boot. Each one
// has a fatal Go-runtime default, so dropping one from the set is not a
// silent feature loss: that signal would kill PID 1, and exit(2) as
// PID 1 is `Kernel panic - not syncing: Attempted to kill init!`.
//
// The functional suite cannot cover this. Sending one of these to a
// healthy PID 1 correctly reboots or powers off the VM, and a guest
// cannot tell its own orderly shutdown from its own panic.
func TestShutdownSignalSet(t *testing.T) {
	want := []syscall.Signal{
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGHUP,
		syscall.SIGUSR1,
		syscall.SIGUSR2,
	}
	want = append(want, extraShutdownSignals()...)

	seen := make(map[os.Signal]bool)
	for _, s := range shutdownSignalSet() {
		seen[s] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("shutdownSignalSet is missing %v — unclaimed, it would kill PID 1", w)
		}
	}
}

// TestShutdownSignalSetExcludesSIGCHLD guards the reason SetupEarlySignals
// exists as its own function: SIGCHLD must wait for Run, or orphan reaping
// fills the buffer during boot and os/signal drops the Ctrl+Alt+Del.
func TestShutdownSignalSetExcludesSIGCHLD(t *testing.T) {
	for _, s := range shutdownSignalSet() {
		if s == syscall.SIGCHLD {
			t.Fatal("shutdownSignalSet must not contain SIGCHLD — see SetupEarlySignals")
		}
	}
}
