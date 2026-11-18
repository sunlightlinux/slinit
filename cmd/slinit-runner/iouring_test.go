//go:build linux

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/internal/iouringtest"
	"github.com/sunlightlinux/slinit/pkg/seccomp"
)

// The runner's io_uring step on the real kernel, through run() itself.
//
// The service gets restrict-address-families = AF_UNIX and a system call
// filter that refuses io_uring_register(). The io_uring filters have to be
// registered before that filter is installed, or registering them is
// refused and the service could create an AF_INET socket through a ring.
// The exec'd target is this test binary, which makes that request and
// reports what it got.
const (
	runnerArgsEnv  = "SLINIT_TEST_RUNNER_IOURING"
	targetProbeEnv = "SLINIT_TEST_IOURING_TARGET"
	supportEnv     = "SLINIT_TEST_IOURING_SUPPORT"
	exitNoFilters  = 42
)

func init() {
	if os.Getenv(supportEnv) != "" {
		// Probed in a child of its own: the target cannot probe, since
		// its filter refuses io_uring_register(), the probe included.
		if seccomp.EnsureNoNewPrivs() != nil || !seccomp.IOUringFilterSupported() {
			os.Exit(exitNoFilters)
		}
		if _, err := iouringtest.Socket(unix.AF_UNIX); err != nil {
			os.Exit(exitNoFilters)
		}
		os.Exit(0)
	}
	if os.Getenv(targetProbeEnv) != "" {
		r, err := iouringtest.Socket(unix.AF_INET)
		switch {
		case err != nil:
			os.Exit(exitNoFilters) // io_uring itself unavailable
		case r == -int32(unix.EACCES):
			os.Exit(0)
		default:
			os.Stderr.WriteString("AF_INET socket through io_uring was not refused\n")
			os.Exit(3)
		}
	}
	if os.Getenv(runnerArgsEnv) != "" {
		os.Setenv(targetProbeEnv, "1")
		os.Args = []string{"slinit-runner",
			"--syscall-filter", "~io_uring_register", "--syscall-action", "EPERM",
			"--restrict-address-family", "AF_UNIX", "--restrict-address-families-enable",
			"--", os.Args[0], "-test.run=^$"}
		if err := run(); err != nil {
			os.Stderr.WriteString("runner: " + err.Error() + "\n")
			os.Exit(2)
		}
	}
}

func TestRunnerRegistersIOUringFiltersBeforeSeccomp(t *testing.T) {
	probe := exec.Command(os.Args[0], "-test.run=^$")
	probe.Env = append(os.Environ(), supportEnv+"=1")
	if err := probe.Run(); err != nil {
		t.Skipf("io_uring BPF filters not available on this kernel (%v)", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), runnerArgsEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws := ee.Sys().(syscall.WaitStatus); ws.Signaled() {
			t.Fatalf("killed by %v\n%s", ws.Signal(), out)
		}
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
