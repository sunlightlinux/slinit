package process

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The wait for an ad-hoc child must stay on that child even when PID 1's
// reaper collected it before anyone registered its pid and the pid then
// went to another process. A pidfd opened after Start by pid named the
// newcomer, and the wait parked on it for as long as it ran — with no
// grace timeout, so a hook or an ifup call hung on an unrelated daemon.
//
// Runs as PID 1 of a fresh user+pid namespace, where ns_last_pid can
// steer the next fork onto the freed pid.
func TestRunAdhocSurvivesPidReuse(t *testing.T) {
	if os.Getenv("SLINIT_ADHOC_PIDREUSE_CHILD") == "1" {
		adhocPidReuseChild()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunAdhocSurvivesPidReuse$")
	cmd.Env = append(os.Environ(), "SLINIT_ADHOC_PIDREUSE_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWPID,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "SKIP:") {
		t.Skipf("%s", strings.TrimSpace(string(out)))
	}
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") ||
			strings.Contains(err.Error(), "invalid argument") {
			t.Skipf("user namespaces unavailable: %v", err)
		}
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PIDREUSE-OK") {
		t.Fatalf("child did not finish:\n%s", out)
	}
}

func adhocPidReuseChild() {
	fail := func(f string, a ...any) {
		fmt.Printf("FAIL: "+f+"\n", a...)
		os.Exit(1)
	}
	waitStatusGrace = 500 * time.Millisecond

	var intruder *exec.Cmd
	defer func() {
		if intruder != nil {
			intruder.Process.Kill()
			intruder.Wait()
		}
	}()
	var reused bool
	intruderPid := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		c := exec.Command("/bin/true")
		done <- RunAdhocStarted(c, func(pid int) {
			// PID 1's reaper collects the child before RunAdhoc has
			// registered it: the status reaches nobody.
			var ws syscall.WaitStatus
			if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
				fail("wait4 %d: %v", pid, err)
			}
			// Hand the pid to a long-lived unrelated process. The
			// runtime's own thread creation can take the pid first, so
			// try a few times.
			for i := 0; i < 20 && !reused; i++ {
				if err := os.WriteFile("/proc/sys/kernel/ns_last_pid",
					[]byte(strconv.Itoa(pid-1)), 0); err != nil {
					fmt.Printf("SKIP: ns_last_pid: %v\n", err)
					os.Exit(0)
				}
				s := exec.Command("/bin/sleep", "30")
				if err := s.Start(); err != nil {
					fail("start sleep: %v", err)
				}
				if s.Process.Pid == pid {
					intruder, reused = s, true
					intruderPid <- pid
					break
				}
				s.Process.Kill()
				s.Wait()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		fail("RunAdhoc still blocked after 10s: parked on the unrelated pid %d (sleep 30)",
			<-intruderPid)
	}
	if !reused {
		fmt.Println("SKIP: could not steer the freed pid to another process")
		os.Exit(0)
	}
	fmt.Println("PIDREUSE-OK")
}

// The pidfd PrepareAdhoc asks for belongs to the caller, not to os: the
// wait must close it, or every hook, ifup and logger restart leaks a
// descriptor in PID 1.
func TestRunAdhocClosesPidfd(t *testing.T) {
	count := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Skip(err)
		}
		return len(ents)
	}
	RunAdhoc(exec.Command("/bin/true")) // let os probe pidfd support first
	before := count()
	for i := 0; i < 50; i++ {
		if err := RunAdhoc(exec.Command("/bin/true")); err != nil {
			t.Fatal(err)
		}
	}
	if after := count(); after > before+5 {
		t.Errorf("%d open fds after 50 RunAdhoc calls, %d before: the pidfd leaks", after, before)
	}
}
