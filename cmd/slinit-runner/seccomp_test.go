//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The runner's system-call filter on the real kernel, through run()
// itself: a child of this test binary runs the runner with real
// arguments, and the runner execs this test binary again as the service,
// which calls getppid() and checks the answer against what the case
// expects.
const (
	seccompCaseEnv   = "SLINIT_TEST_RUNNER_SECCOMP"
	seccompTargetEnv = "SLINIT_TEST_SECCOMP_TARGET"
)

var seccompCases = map[string]struct {
	args  []string
	errno unix.Errno // what getppid() must fail with; 0 = it must succeed
}{
	// system-call-log only logs: a call it names must still be refused
	// by the filter, as under systemd, which installs the log list as a
	// filter of its own.
	"log-keeps-deny": {[]string{"--syscall-filter", "~getppid", "--syscall-action", "EPERM",
		"--syscall-log", "getppid"}, unix.EPERM},
	// Without a system-call-filter list there is nothing to refuse: the
	// architecture check, the log list or the error number alone must
	// not kill the service at its first system call.
	"arch-alone":   {[]string{"--syscall-arch", "native"}, 0},
	"log-alone":    {[]string{"--syscall-log", "getppid"}, 0},
	"action-alone": {[]string{"--syscall-action", "EPERM"}, 0},
}

func init() {
	switch {
	case os.Getenv(seccompTargetEnv) != "":
		want := seccompCases[os.Getenv(seccompTargetEnv)].errno
		_, _, e := unix.RawSyscall(unix.SYS_GETPPID, 0, 0, 0)
		if e != want {
			fmt.Fprintf(os.Stderr, "getppid: errno %d (%v), want %d (%v)\n", e, e, want, want)
			os.Exit(3)
		}
		os.Exit(0)

	case os.Getenv(seccompCaseEnv) != "":
		c := os.Getenv(seccompCaseEnv)
		os.Setenv(seccompTargetEnv, c)
		os.Args = append(append([]string{"slinit-runner"}, seccompCases[c].args...), "--", os.Args[0], "-test.run=^$")
		if err := run(); err != nil {
			os.Stderr.WriteString("runner: " + err.Error() + "\n")
			os.Exit(2)
		}
	}
}

func runSeccompCase(t *testing.T, c string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), seccompCaseEnv+"="+c)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws := ee.Sys().(syscall.WaitStatus); ws.Signaled() {
			t.Fatalf("%s: service killed by %v\n%s", c, ws.Signal(), out)
		}
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", c, err, out)
	}
}

func TestRunnerSyscallLogKeepsDeny(t *testing.T) { runSeccompCase(t, "log-keeps-deny") }

func TestRunnerSyscallArchAloneAllows(t *testing.T)   { runSeccompCase(t, "arch-alone") }
func TestRunnerSyscallLogAloneAllows(t *testing.T)    { runSeccompCase(t, "log-alone") }
func TestRunnerSyscallActionAloneAllows(t *testing.T) { runSeccompCase(t, "action-alone") }
