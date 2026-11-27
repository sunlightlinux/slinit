package service

import (
	"os"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// countPidfds counts this process's open pidfds.
func countPidfds(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	n := 0
	for _, e := range ents {
		if l, err := os.Readlink("/proc/self/fd/" + e.Name()); err == nil && strings.Contains(l, "pidfd") {
			n++
		}
	}
	return n
}

// When PID 1's orphan reaper collected an output-logger before the
// supervisor's wait did, the wait failed and os kept the logger's pidfd
// until a garbage collection. Soaked with slinit as PID 1 and GOGC=off,
// killing a service's logger once a second: 4 more pidfds open after 60
// respawns, growing steadily. The reaper here is PID 1's (Wait4(-1)
// then Route) running hot, so it wins nearly every time.
func TestReapedOutputLoggerDoesNotKeepItsPidfd(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for three logger restarts, 1s apart")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	stop := make(chan struct{})
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var st syscall.WaitStatus
			if pid, err := syscall.Wait4(-1, &st, syscall.WNOHANG, nil); pid > 0 && err == nil {
				process.DefaultExitRouter.Route(pid, st)
			}
		}
	}()
	defer func() { close(stop); <-reaped }()

	before := countPidfds(t)
	w, ls, err := startLoggerSupervisor([]string{"/bin/true"}, "pidfd-logger",
		"output-logger", &testLogger{}, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	// The first logger and three restarts, each exiting at once.
	time.Sleep(3500 * time.Millisecond)
	ls.Stop()
	<-ls.done

	var leaked int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if leaked = countPidfds(t) - before; leaked <= 0 {
			return
		}
	}
	t.Errorf("%d pidfds still open after the output-logger exited 4 times under a reaper; want 0", leaked)
}
