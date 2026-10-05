package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// A logger command used to be started once and forgotten: the parent
// closed the pipe's read end right after the fork and reaped the child
// only so it would not zombie. When the logger then exited — crashed,
// was killed, or simply returned — the pipe had no reader left and the
// SERVICE died of SIGPIPE on its next write. Measured against a logger
// that exits after one second: "process killed by signal broken pipe",
// and with restart = yes, four kills and four restarts in eight
// seconds. A log consumer taking down the thing it logs for is the
// wrong failure direction, and nothing in the daemon noticed.
//
// So slinit keeps the read end open for as long as the service lives
// and hands that same descriptor to each logger it starts. Two things
// follow from holding it rather than relaying through slinit:
//
//   - the service can never see EPIPE, because a reader always exists;
//   - a restarted logger inherits whatever is still in the pipe, so the
//     lines written while it was down are delivered rather than lost.
//
// The cost is that a pipe nobody drains eventually fills and the
// service blocks writing to it. That is why giving up is a state and
// not an error: after restartLimit consecutive failures the supervisor
// drains the pipe itself, which keeps the service running — logs are
// being dropped at that point, and it says so once, loudly.
const (
	// Matches immortal's logger restart pause. Long enough that a
	// logger failing on every exec cannot spin, short enough that the
	// 64 KiB pipe buffer absorbs a busy service's output meanwhile.
	loggerRestartDelay = time.Second

	// Consecutive failures before the supervisor stops trying. A
	// logger that cannot survive five attempts is misconfigured, not
	// unlucky.
	loggerRestartLimit = 5
)

// loggerSupervisor keeps one logger command running for a service.
type loggerSupervisor struct {
	label   string // "output-logger" / "error-logger", for messages
	svcName string
	argv    []string
	readEnd *os.File // pipe read end, handed to every logger we start
	logger  ServiceLogger

	// running reports whether the service still has a process writing
	// to the pipe. When the SERVICE exits, the parent has already
	// closed the write end, so the pipe reaches EOF and the logger
	// exits of its own accord — correctly. Without this check that
	// looked identical to a crash: every normal stop logged an error
	// and queued a pointless restart, in the window before
	// stopLoggerCommands got around to calling Stop.
	running func() bool

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool

	done chan struct{}
}

// startLoggerSupervisor creates the pipe, starts the logger command on
// it and returns the write end for the service to use as stdout/stderr.
// The read end stays with the supervisor.
func startLoggerSupervisor(argv []string, svcName, label string, logger ServiceLogger,
	running func() bool) (*os.File, *loggerSupervisor, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	ls := &loggerSupervisor{
		label:   label,
		svcName: svcName,
		argv:    argv,
		readEnd: r,
		logger:  logger,
		running: running,
		done:    make(chan struct{}),
	}
	cmd, err := ls.spawn()
	if err != nil {
		r.Close()
		w.Close()
		return nil, nil, err
	}
	ls.cmd = cmd
	go ls.supervise()
	return w, ls, nil
}

// spawn starts one instance of the logger reading from readEnd. Caller
// must not hold mu.
func (ls *loggerSupervisor) spawn() (*exec.Cmd, error) {
	cmd := exec.CommandContext(context.Background(), ls.argv[0], ls.argv[1:]...)
	cmd.Stdin = ls.readEnd
	// The logger's own stdout/stderr go nowhere: a logger that writes
	// to its stdout while being the service's stdout is a feedback loop.
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// supervise waits for the current logger and restarts it when it exits
// of its own accord. Returns once the supervisor is stopped or has given
// up and fallen back to draining.
func (ls *loggerSupervisor) supervise() {
	defer close(ls.done)

	failures := 0
	for {
		if cmd := ls.current(); cmd != nil {
			// RunAdhoc's sibling problem: PID 1's reaper can collect
			// this child before Wait sees it. WaitAdhoc registers it
			// with the exit router so the real status comes back
			// instead of ECHILD.
			waitErr := process.WaitAdhoc(cmd)
			if ls.isStopped() {
				// We killed it on purpose; nothing to report.
				return
			}
			if ls.running != nil && !ls.running() {
				// The service is gone, so the pipe reached EOF and the
				// logger exited because it had nothing left to read.
				// Expected teardown, not a failure.
				return
			}
			ls.logger.Error("Service '%s': %s %s — restarting it; the service "+
				"keeps running because slinit holds the pipe open",
				ls.svcName, ls.label, describeLoggerExit(waitErr))
		}

		time.Sleep(loggerRestartDelay)
		if ls.isStopped() {
			return
		}

		next, err := ls.spawn()
		if err == nil {
			ls.setCurrent(next)
			failures = 0
			continue
		}
		ls.setCurrent(nil)

		failures++
		if failures >= loggerRestartLimit {
			ls.logger.Error("Service '%s': %s could not be restarted after %d "+
				"attempts (%v) — draining its output and DISCARDING it so the "+
				"service does not block on a full pipe",
				ls.svcName, ls.label, loggerRestartLimit, err)
			ls.drain()
			return
		}
		ls.logger.Error("Service '%s': %s restart failed (%v), attempt %d of %d",
			ls.svcName, ls.label, err, failures, loggerRestartLimit)
	}
}

// current returns the running logger, or nil when none is.
func (ls *loggerSupervisor) current() *exec.Cmd {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.cmd
}

func (ls *loggerSupervisor) setCurrent(cmd *exec.Cmd) {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.stopped && cmd != nil && cmd.Process != nil {
		// Stop raced us; do not leave an unowned child behind.
		_ = cmd.Process.Kill()
		return
	}
	ls.cmd = cmd
}

func (ls *loggerSupervisor) isStopped() bool {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.stopped
}

// drain reads and discards whatever the service writes, so a service
// whose logger is gone for good keeps running instead of blocking on a
// full pipe. Returns when the write end closes (service gone) or the
// supervisor is stopped (readEnd closed under it).
func (ls *loggerSupervisor) drain() {
	_, _ = io.Copy(io.Discard, ls.readEnd)
}

// Stop kills the logger and releases the pipe read end. Idempotent.
func (ls *loggerSupervisor) Stop() {
	ls.mu.Lock()
	if ls.stopped {
		ls.mu.Unlock()
		return
	}
	ls.stopped = true
	cmd := ls.cmd
	ls.cmd = nil
	ls.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// Closing the read end also unblocks a drain() in progress.
	if ls.readEnd != nil {
		_ = ls.readEnd.Close()
	}
}

// PID returns the running logger's pid, or -1. For tests and status.
func (ls *loggerSupervisor) PID() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.cmd != nil && ls.cmd.Process != nil {
		return ls.cmd.Process.Pid
	}
	return -1
}

// hasRunningProcess reports whether the service still has a process
// that could be writing to its log pipe. Read lockless, like every
// other reader of state: it is written under queueMu and a stale read
// here only costs one restart attempt either way.
func (s *ProcessService) hasRunningProcess() bool {
	st := s.state.Load()
	return st == StateStarted || st == StateStarting
}

// describeLoggerExit renders a logger's exit for the operational log.
// A clean exit is the common case — a logger that reads to EOF and
// returns 0 — and "exited (<nil>)" is a poor way to say so.
func describeLoggerExit(err error) string {
	if err == nil {
		return "exited cleanly"
	}
	if code, ok := process.ExitCodeOf(err); ok {
		return fmt.Sprintf("exited with status %d", code)
	}
	return fmt.Sprintf("exited: %v", err)
}
