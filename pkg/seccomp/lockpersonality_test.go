//go:build linux

package seccomp

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// lockPersonalityChildEnv makes the test binary act as the filtered child
// in TestLockPersonalityOnKernel: it installs the filter and makes the
// one personality() call named in the variable.
const lockPersonalityChildEnv = "SLINIT_TEST_LOCKPERSONALITY_ARG"

func init() {
	arg := os.Getenv(lockPersonalityChildEnv)
	if arg == "" {
		return
	}
	// seccomp filters are per thread without TSYNC: install and call on
	// the same one, as the runner does before it execs.
	runtime.LockOSThread()
	cur, _, errno := unix.Syscall(unix.SYS_PERSONALITY, 0xffffffff, 0, 0)
	if errno != 0 {
		os.Exit(3)
	}
	prog, err := CompileLockPersonality(uint32(cur))
	if err != nil {
		os.Exit(4)
	}
	if EnsureNoNewPrivs() != nil || Install(prog) != nil {
		os.Exit(5)
	}
	v, err := strconv.ParseUint(arg, 0, 32)
	if err != nil {
		os.Exit(6)
	}
	if v == 0 { // "current": set the personality it already has
		v = uint64(cur)
	}
	if _, _, errno := unix.Syscall(unix.SYS_PERSONALITY, uintptr(v), 0, 0); errno == unix.EPERM {
		os.Exit(7) // refused by the filter, and the process lived
	}
	os.Exit(0) // the filter let the call through
}

// Runs the filter on the real kernel: the query and a no-op set pass, and
// a change is refused with EPERM while the process carries on, as under
// systemd. It used to kill the caller with SIGSYS.
func TestLockPersonalityOnKernel(t *testing.T) {
	run := func(arg string) error {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), lockPersonalityChildEnv+"="+arg)
		return cmd.Run()
	}
	for _, c := range []struct {
		arg  string
		desc string
	}{
		{"0xffffffff", "query"},
		{"0", "set the current personality"},
	} {
		if err := run(c.arg); err != nil {
			t.Errorf("%s: child failed (%v), want the call allowed", c.desc, err)
		}
	}

	// PER_LINUX32 differs from the PER_LINUX the test runs with.
	err := run("0x0008")
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("changing the personality: child %v, want refused with EPERM (exit 7)", err)
	}
}

func TestCompileLockPersonality(t *testing.T) {
	prog, err := CompileLockPersonality(0)
	if err != nil {
		t.Fatal(err)
	}
	assertValidBPF(t, "lock-personality", prog)
}
