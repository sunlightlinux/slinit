package eventloop

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
)

// runRemain runs a PID 1 mode loop through a shutdown remain, the way
// main.go leaves one loop before starting the next.
func runRemain(t *testing.T) {
	t.Helper()
	logger := logging.New(logging.LevelError)
	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)
	done := make(chan error, 1)
	go func() { done <- el.Run(context.Background()) }()
	el.InitiateShutdown(service.ShutdownRemain)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after a remain")
	}
}

// procState returns the state letter of pid from /proc, or "" when the
// pid is gone (reaped).
func procState(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	s := string(data)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2 : i+3]
	}
	return ""
}

// Between two loops (a remain, a boot-failure retry) SIGCHLD is not
// claimed, so an orphan that exits then sends a signal nobody receives.
// The next loop never reaped at start, leaving it a zombie until some
// later child happened to exit.
func TestRunReapsOrphansThatExitedBetweenLoops(t *testing.T) {
	runRemain(t)

	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	for deadline := time.Now().Add(2 * time.Second); procState(pid) != "Z"; {
		if time.Now().After(deadline) {
			t.Fatalf("child %d never became a zombie (state %q)", pid, procState(pid))
		}
		time.Sleep(5 * time.Millisecond)
	}

	logger := logging.New(logging.LevelError)
	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)
	done := make(chan error, 1)
	go func() { done <- el.Run(context.Background()) }()
	defer func() {
		el.InitiateShutdown(service.ShutdownRemain)
		<-done
	}()

	for deadline := time.Now().Add(time.Second); procState(pid) != ""; {
		if time.Now().After(deadline) {
			syscall.Wait4(pid, nil, 0, nil)
			t.Fatalf("zombie %d was not reaped by the new loop", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// releaseSignals claims the signals on a fresh channel as PID 1. That
// channel was discarded but stayed registered, so each remain added one
// more that every later signal was copied to. The next loop now lets it
// go once it has claimed the signals itself.
func TestRunReleasesSignalsHeldSinceLastLoop(t *testing.T) {
	runRemain(t)
	held := heldSigCh
	if held == nil {
		t.Fatal("no channel holds the signals after a PID 1 loop returned")
	}

	logger := logging.New(logging.LevelError)
	el := New(service.NewServiceSet(logger), logger)
	el.SetPID1Mode(true)
	reopened := make(chan struct{}, 1)
	el.OnReopenSocket = func() { reopened <- struct{}{} }
	done := make(chan error, 1)
	go func() { done <- el.Run(context.Background()) }()
	defer func() {
		el.InitiateShutdown(service.ShutdownRemain)
		<-done
	}()

	time.Sleep(50 * time.Millisecond) // let Run claim the signals
	syscall.Kill(os.Getpid(), syscall.SIGUSR1)
	select {
	case <-reopened:
	case <-time.After(2 * time.Second):
		t.Fatal("the new loop did not receive SIGUSR1")
	}
	// The signal package copies a signal to every registered channel in
	// one pass, so by now a still-registered held channel has it too.
	if n := len(held); n != 0 {
		t.Errorf("the previous loop's holding channel still receives signals (%d queued)", n)
	}
}
