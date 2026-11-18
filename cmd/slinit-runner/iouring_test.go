//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sunlightlinux/slinit/internal/iouringtest"
	"github.com/sunlightlinux/slinit/pkg/seccomp"
)

// The runner's io_uring step on the real kernel, through run() itself:
// a child of this test binary runs the runner with real arguments, and
// the runner execs this test binary again as the service, which makes one
// io_uring request and reports what it got.
const (
	runnerCaseEnv = "SLINIT_TEST_RUNNER_IOURING"
	targetEnv     = "SLINIT_TEST_IOURING_TARGET"
	supportEnv    = "SLINIT_TEST_IOURING_SUPPORT"
	exitNoFilters = 42
)

var runnerCases = map[string][]string{
	// The service may not create AF_INET sockets and may not call
	// io_uring_register(). The io_uring filters have to be registered
	// before that system call filter is installed, or registering them
	// is refused and the ring could make the socket.
	"order": {"--syscall-filter", "~io_uring_register", "--syscall-action", "EPERM",
		"--restrict-address-family", "AF_UNIX", "--restrict-address-families-enable"},
	"op-filter": {"--io-uring-filter", "~socket"},
	// Same setting on a kernel that cannot filter io_uring.
	"fallback": {"--io-uring-filter", "~socket"},
}

func init() {
	switch {
	case os.Getenv(supportEnv) != "":
		// Probed in a child of its own: under the "order" case the
		// service cannot probe, its filter refuses io_uring_register().
		if seccomp.EnsureNoNewPrivs() != nil || !seccomp.IOUringFilterSupported() {
			os.Exit(exitNoFilters)
		}
		if _, err := iouringtest.Socket(unix.AF_UNIX); err != nil {
			os.Exit(exitNoFilters)
		}
		os.Exit(0)

	case os.Getenv(targetEnv) != "":
		af := unix.AF_INET
		if os.Getenv(targetEnv) != "order" {
			af = unix.AF_UNIX
		}
		r, err := iouringtest.Socket(af)
		if os.Getenv(targetEnv) == "fallback" {
			if errors.Is(err, unix.ENOSYS) {
				os.Exit(0)
			}
			os.Stderr.WriteString("io_uring_setup was not refused with ENOSYS\n")
			os.Exit(3)
		}
		if err == nil && r == -int32(unix.EACCES) {
			os.Exit(0)
		}
		os.Stderr.WriteString("socket through io_uring was not refused\n")
		os.Exit(3)

	case os.Getenv(runnerCaseEnv) != "":
		c := os.Getenv(runnerCaseEnv)
		os.Setenv(targetEnv, c)
		if c == "fallback" {
			ioUringSupported = func() bool { return false }
		}
		os.Args = append(append([]string{"slinit-runner"}, runnerCases[c]...), "--", os.Args[0], "-test.run=^$")
		if err := run(); err != nil {
			os.Stderr.WriteString("runner: " + err.Error() + "\n")
			os.Exit(2)
		}
	}
}

func runRunnerCase(t *testing.T, c string) {
	t.Helper()
	probe := exec.Command(os.Args[0], "-test.run=^$")
	probe.Env = append(os.Environ(), supportEnv+"=1")
	if err := probe.Run(); err != nil {
		t.Skipf("io_uring BPF filters not available on this kernel (%v)", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), runnerCaseEnv+"="+c)
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		if ws := ee.Sys().(syscall.WaitStatus); ws.Signaled() {
			t.Fatalf("%s: killed by %v\n%s", c, ws.Signal(), out)
		}
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", c, err, out)
	}
}

func TestRunnerRegistersIOUringFiltersBeforeSeccomp(t *testing.T)  { runRunnerCase(t, "order") }
func TestRunnerIOUringFilterDirective(t *testing.T)                { runRunnerCase(t, "op-filter") }
func TestRunnerIOUringFilterFallsBackToRefusingSetup(t *testing.T) { runRunnerCase(t, "fallback") }
