package process

import (
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"
)

// openPidfds counts this process's pidfds.
func openPidfds(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range ents {
		if l, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && strings.Contains(l, "pidfd") {
			n++
		}
	}
	return n
}

// When PID 1's orphan reaper collects a managed child before cmd.Wait()
// does, Wait fails with ECHILD, and os.Process keeps its pidfd: only a
// successful wait or Release closes it. Nothing called Release, so each
// such exit left a pidfd open in PID 1 until a GC ran the Process's
// cleanup. Measured with slinit as PID 1 and GOGC=off: a crash-looping
// service, 150 restarts, 40 pidfds open; none as a user instance, which
// has no reaper.
//
// This runs the reaper the way PID 1 does (Wait4(-1) then Route, as in
// TestFastExitKeepsItsStatusUnderAHotReaper) with the collector off, and
// counts pidfds once every child is accounted for.
func TestReapedChildDoesNotKeepItsPidfd(t *testing.T) {
	underHotReaper(t, func() error {
		_, exitCh, err := StartProcess(ExecParams{
			ServiceName: "pidfd-reaped",
			Command:     []string{"/bin/true"},
		})
		if err != nil {
			return err
		}
		<-exitCh
		return nil
	})
}

// The same for an ad-hoc command (hooks, health checks, cron): its wait
// had the same unknown-status branch.
func TestReapedAdhocDoesNotKeepItsPidfd(t *testing.T) {
	underHotReaper(t, func() error {
		err := RunAdhoc(exec.Command("/bin/true"))
		if err != nil {
			return err
		}
		return nil
	})
}

// underHotReaper runs start 300 times with a PID-1-style reaper and the
// collector off, and fails if pidfds were left open.
func underHotReaper(t *testing.T, start func() error) {
	t.Helper()
	if testing.Short() {
		t.Skip("stress test: 300 process starts")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var st syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &st, syscall.WNOHANG, nil)
			if pid > 0 && err == nil {
				DefaultExitRouter.Route(pid, st)
			}
		}
	}()
	defer func() { close(stop); <-done }()

	before := openPidfds(t)
	const n = 300
	for i := 0; i < n; i++ {
		if err := start(); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
	// The cmd.Wait() goroutine of a child the reaper won may still be on
	// its way out; give it a moment.
	var leaked int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if leaked = openPidfds(t) - before; leaked == 0 {
			break
		}
	}
	if leaked > 0 {
		t.Errorf("%d pidfds still open after %d exits under a reaper; want 0", leaked, n)
	}
}
