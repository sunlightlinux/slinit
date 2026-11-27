package process

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// osThreads reads this process's OS thread count from /proc.
func osThreads(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "Threads:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatalf("parse %q: %v", line, err)
			}
			return n
		}
	}
	t.Fatal("no Threads: line in /proc/self/status")
	return 0
}

// openFDs counts this process's open descriptors.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	return len(ents)
}

// Waiting for a child used to park an OS thread for the child's whole
// life: cmd.Wait() blocks in waitid on the child's pidfd. A 2000-service
// boot held ~1000 threads, and with RLIMIT_NPROC (or a container's
// pids.max) close to exhaustion, the Go runtime's next pthread_create
// failed and slinit aborted — a kernel panic when it is PID 1. The thread
// count must not grow with the number of running children.
func TestRunningChildrenDoNotHoldThreads(t *testing.T) {
	if testing.Short() {
		t.Skip("starts 300 processes")
	}
	const n = 300
	before := osThreads(t)
	fdsBefore := openFDs(t)

	type child struct {
		pid int
		ch  <-chan ChildExit
	}
	children := make([]child, 0, n)
	defer func() {
		for _, c := range children {
			syscall.Kill(c.pid, syscall.SIGKILL)
		}
		for _, c := range children {
			<-c.ch
		}
	}()
	for i := 0; i < n; i++ {
		pid, ch, err := StartProcess(ExecParams{
			ServiceName: "thread-count",
			Command:     []string{"/bin/sleep", "1000"},
		})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		children = append(children, child{pid, ch})
	}
	// Let every wait goroutine reach its blocking point.
	time.Sleep(500 * time.Millisecond)

	after := osThreads(t)
	bound := before + runtime.GOMAXPROCS(0) + 30
	t.Logf("threads: %d before, %d with %d children running (bound %d)", before, after, n, bound)
	if after > bound {
		t.Errorf("%d OS threads with %d children running (%d before): waiting parks a thread per child",
			after, n, before)
	}

	// Without a thread, but not at the price of a second descriptor per
	// child: PID 1 starts with a hard RLIMIT_NOFILE of 4096.
	if fds := openFDs(t); fds-fdsBefore > n+20 {
		t.Errorf("%d descriptors with %d children running (%d before): more than one per child",
			fds, n, fdsBefore)
	}

	// And the children still report their real end once they go.
	for _, c := range children[:10] {
		syscall.Kill(c.pid, syscall.SIGTERM)
	}
	for i, c := range children[:10] {
		ex := <-c.ch
		if !ex.Status.Signaled() || ex.Status.Signal() != syscall.SIGTERM {
			t.Errorf("child %d: status %v, want killed by SIGTERM", i, ex.Status)
		}
	}
	children = children[10:]
}

// The pidfd wait reaps the child itself, so it races PID 1's Wait4(-1)
// just as cmd.Wait() did. Whichever wins, the service must see the real
// status: an exit code or a killing signal, never a made-up clean exit.
// Run with and without a hot reaper so both winners are exercised.
func TestExitWatchKeepsRealStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("starts 400 processes")
	}
	for _, tc := range []struct {
		name   string
		reaper func() func()
	}{
		{"own-waitid", nil},
		{"hot-reaper", reaperLoop},
		// Wait4 and Route are separate steps, so the wait can come back
		// empty-handed before the status is routed. It must wait for
		// the route, not report what it does not have.
		{"slow-routing-reaper", func() func() { return slowRoutingReaper(2 * time.Millisecond) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.reaper != nil {
				defer tc.reaper()()
			}
			for i := 0; i < 100; i++ {
				_, ch, err := StartProcess(ExecParams{
					ServiceName: "exit-watch",
					Command:     []string{"/bin/sh", "-c", "exit 7"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if ex := <-ch; !ex.Status.Exited() || ex.Status.ExitStatus() != 7 {
					t.Fatalf("iteration %d: status %#x, want exit 7", i, uint32(ex.Status))
				}

				pid, ch, err := StartProcess(ExecParams{
					ServiceName: "exit-watch",
					Command:     []string{"/bin/sleep", "1000"},
				})
				if err != nil {
					t.Fatal(err)
				}
				syscall.Kill(pid, syscall.SIGUSR1)
				if ex := <-ch; !ex.Status.Signaled() || ex.Status.Signal() != syscall.SIGUSR1 {
					t.Fatalf("iteration %d: status %#x, want killed by SIGUSR1", i, uint32(ex.Status))
				}
			}
		})
	}
}

// siginfo as waitid fills it must convert to what wait4 would report.
func TestSiginfoChildWaitStatus(t *testing.T) {
	mk := func(code, status int32) *siginfoChild {
		si := &siginfoChild{Signo: int32(syscall.SIGCHLD), Status: status}
		si.ErrnoCode[codeIndex] = code
		return si
	}
	if ws := mk(cldExited, 7).waitStatus(); !ws.Exited() || ws.ExitStatus() != 7 {
		t.Errorf("exited 7: got %#x", uint32(ws))
	}
	if ws := mk(cldExited, 0).waitStatus(); !ws.Exited() || ws.ExitStatus() != 0 {
		t.Errorf("exited 0: got %#x", uint32(ws))
	}
	if ws := mk(cldKilled, int32(syscall.SIGTERM)).waitStatus(); !ws.Signaled() || ws.Signal() != syscall.SIGTERM || ws.CoreDump() {
		t.Errorf("killed: got %#x", uint32(ws))
	}
	if ws := mk(cldDumped, int32(syscall.SIGQUIT)).waitStatus(); !ws.Signaled() || ws.Signal() != syscall.SIGQUIT || !ws.CoreDump() {
		t.Errorf("dumped: got %#x", uint32(ws))
	}
}

// slowRoutingReaper is reaperLoop with a delay between reaping a child
// and routing its status.
func slowRoutingReaper(delay time.Duration) func() {
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
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid > 0 && err == nil {
				time.Sleep(delay)
				DefaultExitRouter.Route(pid, ws)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
