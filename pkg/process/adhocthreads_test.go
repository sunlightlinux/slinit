package process

import (
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// WaitAdhoc is how an output logger's supervisor waits for the logger,
// for as long as its service runs, and cmd.Wait() held an OS thread in
// waitid all that time: 500 services with an output-logger left slinit
// with 514 threads, each a task the kernel can refuse the runtime when
// RLIMIT_NPROC or pids.max is close. The count must not grow with the
// number of ad-hoc children being waited for.
func TestWaitAdhocDoesNotHoldThreads(t *testing.T) {
	if testing.Short() {
		t.Skip("starts 300 processes")
	}
	const n = 300
	before := osThreads(t)

	cmds := make([]*exec.Cmd, 0, n)
	errs := make(chan error, n)
	defer func() {
		for _, c := range cmds {
			c.Process.Signal(syscall.SIGKILL)
		}
		for range cmds {
			<-errs
		}
	}()
	for i := 0; i < n; i++ {
		c := exec.Command("/bin/sleep", "1000")
		if err := c.Start(); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		cmds = append(cmds, c)
		go func() { errs <- WaitAdhoc(c) }()
	}
	time.Sleep(500 * time.Millisecond)

	after := osThreads(t)
	bound := before + runtime.GOMAXPROCS(0) + 30
	t.Logf("threads: %d before, %d while waiting for %d children (bound %d)", before, after, n, bound)
	if after > bound {
		t.Errorf("%d OS threads while waiting for %d ad-hoc children (%d before): a thread per wait",
			after, n, before)
	}
}

// Waiting in the netpoller first must not change what WaitAdhoc
// reports, whoever reaps the child: cmd.Wait() itself, PID 1's reaper
// before the pidfd is opened, or the reaper while the wait is parked.
func TestWaitAdhocKeepsRealStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("starts 300 processes")
	}
	for _, tc := range []struct {
		name   string
		reaper func() func()
	}{
		{"own-wait", nil},
		{"hot-reaper", reaperLoop},
		{"slow-routing-reaper", func() func() { return slowRoutingReaper(2 * time.Millisecond) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.reaper != nil {
				defer tc.reaper()()
			}
			for i := 0; i < 50; i++ {
				c := exec.Command("/bin/sh", "-c", "exit 7")
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				if code, ok := ExitCodeOf(WaitAdhoc(c)); !ok || code != 7 {
					t.Fatalf("iteration %d: exit code %d (ok=%v), want 7", i, code, ok)
				}

				c = exec.Command("/bin/sleep", "1000")
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				pid := c.Process.Pid
				go func() {
					time.Sleep(time.Millisecond)
					syscall.Kill(pid, syscall.SIGUSR1)
				}()
				if code, ok := ExitCodeOf(WaitAdhoc(c)); !ok || code != -1 {
					t.Fatalf("iteration %d: exit code %d (ok=%v), want -1 (signalled)", i, code, ok)
				}

				c = exec.Command("/bin/true")
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				if err := WaitAdhoc(c); err != nil {
					t.Fatalf("iteration %d: /bin/true: %v", i, err)
				}
			}
		})
	}
}
