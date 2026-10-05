package process

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Ad-hoc command execution that survives PID 1's orphan reaper.
//
// slinit as PID 1 reaps on SIGCHLD with Wait4(-1, WNOHANG) in a loop,
// which collects ANY zombie — including a child that os/exec is in the
// middle of waiting on. When that happens cmd.Wait() returns
// "waitid: no child processes" (ECHILD) and the caller sees a failure
// for a command that ran perfectly. StartProcess has guarded against
// this for managed service children since the lost-exit-status fix, by
// registering the pid with DefaultExitRouter so the reaper hands the
// real status back. Every other command the daemon runs — the
// env-generator, ready-check, health-check, cron, logrotate's
// processor, exec-conditions, boot and shutdown hooks, rc.local, ifup
// — called cmd.Run()/Output() directly and had no such guard.
//
// The window is narrow, because the reaper only runs when a SIGCHLD is
// delivered and cmd.Wait() is normally already parked in waitid when the
// kernel wakes it. Narrow is not closed: a tight reaper loop takes the
// child about 80% of the time, and functional case 163-env-generator —
// whose service is restarted while other children are exiting — failed
// in CI and in roughly one local run in seven, reporting
// "SLINIT_EG_TEST missing from env" because the generator's output was
// thrown away with the phantom error.
//
// RunAdhoc and OutputAdhoc stand in for cmd.Run() and cmd.Output() and
// take the routed status when cmd.Wait() has none to give.

// adhocExitError reports a non-zero exit reconstructed from a status the
// orphan reaper routed back to us. It is deliberately NOT an
// *exec.ExitError: that type can only be built from an os.ProcessState,
// which cannot be synthesised outside os/exec. Callers that want the
// code should use ExitCodeOf rather than a type assertion.
type adhocExitError struct {
	status syscall.WaitStatus
}

func (e *adhocExitError) Error() string {
	if e.status.Signaled() {
		return fmt.Sprintf("signal: %v", e.status.Signal())
	}
	return fmt.Sprintf("exit status %d", e.status.ExitStatus())
}

// ExitCode returns the process exit code, or -1 when it was signalled.
func (e *adhocExitError) ExitCode() int {
	if e.status.Signaled() {
		return -1
	}
	return e.status.ExitStatus()
}

// ExitCodeOf extracts an exit code from an error returned by RunAdhoc or
// OutputAdhoc, covering both the os/exec error and the reconstructed
// one. It returns ok=false when err does not carry an exit status at
// all, which is the honest answer for an exec failure or an I/O error —
// the caller must not read that as "exited 0".
func ExitCodeOf(err error) (code int, ok bool) {
	if err == nil {
		return 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	var ae *adhocExitError
	if errors.As(err, &ae) {
		return ae.ExitCode(), true
	}
	return 0, false
}

// RunAdhoc is cmd.Run() for a command run inside the daemon: it starts
// the command, registers the child with the exit router so PID 1's
// reaper cannot silently take it, and waits.
//
// Pass a cmd that has not been started. Everything else about it —
// Stdout, Stderr, Env, Dir, the context from CommandContext — behaves
// as it does under cmd.Run().
func RunAdhoc(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	return waitAdhoc(cmd)
}

// OutputAdhoc is cmd.Output() with the same guard. cmd.Stdout must be
// nil, as with Output().
func OutputAdhoc(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("process: OutputAdhoc: Stdout already set")
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := RunAdhoc(cmd)
	return stdout.Bytes(), err
}

// CombinedOutputAdhoc is cmd.CombinedOutput() with the same guard.
func CombinedOutputAdhoc(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Stdout != nil {
		return nil, errors.New("process: CombinedOutputAdhoc: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("process: CombinedOutputAdhoc: Stderr already set")
	}
	var both bytes.Buffer
	cmd.Stdout = &both
	cmd.Stderr = &both
	err := RunAdhoc(cmd)
	return both.Bytes(), err
}

// WaitAdhoc is cmd.Wait() with the same guard, for callers that need
// Start and Wait apart — a pipe to drain, say. Registration is then
// later than RunAdhoc's, but the router holds a status reaped before
// anyone registered, which is what that table exists for.
func WaitAdhoc(cmd *exec.Cmd) error { return waitAdhoc(cmd) }

// waitAdhoc waits for an already-started cmd, preferring cmd.Wait()'s
// own answer and falling back to the router when it has none.
//
// Registration happens after Start, which is the earliest the pid
// exists; the router holds a status reaped in between, so arriving late
// is survivable. cmd.Wait() is still left to do the work in the common
// case: it also drains the Stdout/Stderr copy goroutines, and it does
// that even when the wait itself failed, so a stolen child's output is
// complete — only its status is missing.
func waitAdhoc(cmd *exec.Cmd) error {
	pid := cmd.Process.Pid
	routed := DefaultExitRouter.Register(pid)
	defer DefaultExitRouter.Unregister(pid)

	type result struct {
		err   error
		known bool // err (or its absence) reflects a real wait status
	}
	waitDone := make(chan result, 1)
	go func() {
		err := cmd.Wait()
		var ee *exec.ExitError
		switch {
		case err == nil:
			waitDone <- result{nil, true}
		case errors.As(err, &ee):
			waitDone <- result{err, true}
		default:
			// No status: either the reaper took the child (ECHILD) or
			// something else went wrong. Say so rather than passing a
			// phantom failure to the caller.
			waitDone <- result{err, false}
		}
	}()

	res := <-waitDone
	if res.known {
		return res.err
	}
	// cmd.Wait() lost the child. The real status went to the reaper,
	// which routes it here — already in the router's pending table, or
	// arriving within microseconds.
	select {
	case status := <-routed:
		if status.Signaled() || status.ExitStatus() != 0 {
			return &adhocExitError{status: status}
		}
		return nil
	case <-time.After(waitStatusGrace):
		// Should be unreachable; keep the original error rather than
		// inventing success, and be loud about why.
		fmt.Fprintf(os.Stderr,
			"slinit: pid %d: ad-hoc command status lost — cmd.Wait() gave "+
				"none and none was routed within %s: %v\n",
			pid, waitStatusGrace, res.err)
		return res.err
	}
}
