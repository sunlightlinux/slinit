//go:build linux

package seccomp

import (
	"testing"

	"golang.org/x/sys/unix"
)

// What each restrict-* filter answers, call by call, run through the
// compiled program (runBPF). The expectations are systemd's
// (src/shared/seccomp-util.c): a refused call fails with an error a
// program can handle — EPERM, EAFNOSUPPORT, or ENOSYS where the caller is
// meant to fall back to an older call — and is never killed. slinit used
// to kill, which took down any program that probes and recovers.

var (
	allow       = ActionAllow
	eperm       = ErrnoAction(unix.EPERM)
	enosys      = ErrnoAction(unix.ENOSYS)
	eafnosupprt = ErrnoAction(unix.EAFNOSUPPORT)
)

type call struct {
	name string
	args []uint64
	want Action
}

func checkCalls(t *testing.T, filter string, prog []unix.SockFilter, calls []call) {
	t.Helper()
	assertValidBPF(t, filter, prog)
	for _, c := range calls {
		if got := runBPF(t, prog, nrOf(t, c.name), c.args...); got != c.want {
			t.Errorf("%s: %s%v = %#x, want %#x", filter, c.name, c.args, uint32(got), uint32(c.want))
		}
	}
}

const (
	schedOther    = 0
	schedFIFO     = 1
	schedRR       = 2
	schedBatch    = 3
	schedIdle     = 5
	schedDeadline = 6

	sISUID  = 0o4000
	sISGID  = 0o2000
	oCREAT  = unix.O_CREAT
	oRDONLY = 0
)

func TestRestrictSUIDSGIDAnswers(t *testing.T) {
	prog, err := CompileRestrictSUIDSGID()
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, "restrict-suidsgid", prog, []call{
		{"chmod", []uint64{0, 0o4755}, eperm},
		{"chmod", []uint64{0, 0o2755}, eperm},
		{"chmod", []uint64{0, 0o755}, allow},
		{"fchmod", []uint64{3, 0o4755}, eperm},
		{"fchmodat", []uint64{3, 0, 0o6755}, eperm},
		{"fchmodat", []uint64{3, 0, 0o644}, allow},
		{"fchmodat2", []uint64{3, 0, 0o4755, 0}, eperm},
		// Creating a file or directory with the bits set is the same
		// thing by another door; systemd closes these too.
		{"mkdir", []uint64{0, 0o2775}, eperm},
		{"mkdir", []uint64{0, 0o775}, allow},
		{"mkdirat", []uint64{3, 0, 0o2775}, eperm},
		{"mknod", []uint64{0, unix.S_IFREG | 0o4755, 0}, eperm},
		{"mknodat", []uint64{3, 0, unix.S_IFREG | 0o4755, 0}, eperm},
		{"open", []uint64{0, oCREAT | unix.O_WRONLY, 0o4755}, eperm},
		{"open", []uint64{0, oRDONLY, 0o4755}, allow}, // mode ignored without O_CREAT
		{"open", []uint64{0, oCREAT | unix.O_WRONLY, 0o644}, allow},
		{"openat", []uint64{3, 0, oCREAT | unix.O_WRONLY, 0o2755}, eperm},
		{"openat", []uint64{3, 0, oRDONLY, 0o2755}, allow},
		{"creat", []uint64{0, 0o4755}, eperm},
		{"creat", []uint64{0, 0o644}, allow},
		// openat2 hides its flags in a struct: refused with ENOSYS so
		// the program falls back to openat, which is checked.
		{"openat2", []uint64{3, 0, 0, 24}, enosys},
		{"read", []uint64{0, 0, 0}, allow},
	})
}

func TestRestrictAddressFamiliesAnswers(t *testing.T) {
	prog, err := CompileRestrictAddressFamilies([]int{unix.AF_UNIX})
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, "restrict-address-families AF_UNIX", prog, []call{
		{"socket", []uint64{unix.AF_UNIX, unix.SOCK_STREAM, 0}, allow},
		{"socket", []uint64{unix.AF_INET, unix.SOCK_STREAM, 0}, eafnosupprt},
		{"socket", []uint64{unix.AF_INET6, unix.SOCK_DGRAM, 0}, eafnosupprt},
		// systemd leaves socketpair alone: it only makes connected
		// AF_UNIX pairs, which programs use internally.
		{"socketpair", []uint64{unix.AF_UNIX, unix.SOCK_STREAM, 0, 0}, allow},
	})
	none, err := CompileRestrictAddressFamilies(nil)
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, "restrict-address-families (none)", none, []call{
		{"socket", []uint64{unix.AF_UNIX, unix.SOCK_STREAM, 0}, eafnosupprt},
		{"socketpair", []uint64{unix.AF_UNIX, unix.SOCK_STREAM, 0, 0}, allow},
	})
}

func TestRestrictNamespacesAnswers(t *testing.T) {
	prog, err := CompileRestrictNamespaces()
	if err != nil {
		t.Fatal(err)
	}
	const threadFlags = unix.CLONE_VM | unix.CLONE_FS | unix.CLONE_FILES |
		unix.CLONE_SIGHAND | unix.CLONE_THREAD | unix.CLONE_SYSVSEM
	checkCalls(t, "restrict-namespaces", prog, []call{
		// glibc's pthread_create tries clone3 first and falls back to
		// clone on ENOSYS. Killing here killed every threaded service.
		{"clone3", []uint64{0, 88}, enosys},
		{"clone", []uint64{threadFlags, 0, 0, 0, 0}, allow},
		{"clone", []uint64{unix.CLONE_NEWNET, 0, 0, 0, 0}, eperm},
		{"unshare", []uint64{unix.CLONE_NEWNS}, eperm},
		{"unshare", []uint64{unix.CLONE_FILES}, allow},
		{"setns", []uint64{3, unix.CLONE_NEWNET}, eperm},
		// nstype 0 joins whatever namespace the fd names: refusing only
		// the flagged forms left this open.
		{"setns", []uint64{3, 0}, eperm},
	})
}

func TestRestrictRealtimeAnswers(t *testing.T) {
	prog, err := CompileRestrictRealtime()
	if err != nil {
		t.Fatal(err)
	}
	const resetOnFork = 0x40000000
	checkCalls(t, "restrict-realtime", prog, []call{
		{"sched_setscheduler", []uint64{0, schedOther, 0}, allow},
		{"sched_setscheduler", []uint64{0, schedBatch, 0}, allow},
		{"sched_setscheduler", []uint64{0, schedIdle, 0}, allow},
		{"sched_setscheduler", []uint64{0, schedOther | resetOnFork, 0}, allow},
		{"sched_setscheduler", []uint64{0, schedFIFO, 0}, eperm},
		{"sched_setscheduler", []uint64{0, schedRR | resetOnFork, 0}, eperm},
		{"sched_setscheduler", []uint64{0, schedDeadline, 0}, eperm},
		{"sched_setscheduler", []uint64{0, 4, 0}, eperm}, // not an allowed policy
		{"sched_setscheduler", []uint64{0, 7, 0}, eperm}, // nor any future one
		{"sched_setattr", []uint64{0, 0, 0}, eperm},
	})
}

func TestLockPersonalityAnswers(t *testing.T) {
	prog, err := CompileLockPersonality(0)
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, "lock-personality", prog, []call{
		{"personality", []uint64{0xffffffff}, allow},
		{"personality", []uint64{0}, allow},
		{"personality", []uint64{0x0008}, eperm},
	})
}

func TestRestrictFileSystemsAnswers(t *testing.T) {
	prog, err := CompileRestrictFileSystems()
	if err != nil {
		t.Fatal(err)
	}
	checkCalls(t, "restrict-file-systems", prog, []call{
		{"mount", []uint64{0, 0, 0, 0, 0}, eperm},
		{"read", []uint64{0, 0, 0}, allow},
	})
}
