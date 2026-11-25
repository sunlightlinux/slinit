//go:build linux

package seccomp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The restrict-* filters on the real kernel. Each case runs in a child of
// the test binary (restrictChildEnv names it), which sets no_new_privs,
// installs the filter on its thread and makes the calls; it exits 0 when
// every call got the answer systemd gives, and otherwise says which did
// not. A killed child is the old behaviour, and fails the case.
const restrictChildEnv = "SLINIT_TEST_RESTRICT_CASE"

// restrictChildSkip is the child's exit status for "this environment
// cannot run the case" (77, as the functional and container suites use).
const restrictChildSkip = 77

func init() {
	c := os.Getenv(restrictChildEnv)
	if c == "" {
		return
	}
	runtime.LockOSThread()
	fail := func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
	install := func(prog []unix.SockFilter, err error) {
		if err != nil {
			fail("compile: %v", err)
		}
		if EnsureNoNewPrivs() != nil || Install(prog) != nil {
			fail("install failed")
		}
	}
	want := func(what string, got, want unix.Errno) {
		if got != want {
			fail("%s: errno %v, want %v", what, got, want)
		}
	}
	switch c {
	case "namespaces":
		install(CompileRestrictNamespaces())
		_, _, e := unix.Syscall(unix.SYS_CLONE3, 0, 0, 0)
		want("clone3", e, unix.ENOSYS)
		_, _, e = unix.Syscall(unix.SYS_UNSHARE, unix.CLONE_NEWUSER, 0, 0)
		want("unshare(CLONE_NEWUSER)", e, unix.EPERM)
		_, _, e = unix.Syscall(unix.SYS_SETNS, 0, 0, 0)
		want("setns(fd, 0)", e, unix.EPERM)
		// A real threaded program: glibc's pthread_create tries clone3
		// and falls back to clone on ENOSYS.
		if py := os.Getenv("SLINIT_TEST_PYTHON"); py != "" {
			syscall.Exec(py, []string{py, "-c",
				"import threading; t=threading.Thread(target=lambda: None); t.start(); t.join()"}, os.Environ())
			fail("exec python: did not exec")
		}
	case "address-families":
		install(CompileRestrictAddressFamilies([]int{unix.AF_UNIX}))
		_, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		want("socket(AF_INET)", errnoOf(err), unix.EAFNOSUPPORT)
		if fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0); err != nil {
			fail("socket(AF_UNIX): %v", err)
		} else {
			unix.Close(fd)
		}
		if _, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0); err != nil {
			fail("socketpair(AF_UNIX): %v", err)
		}
	case "suid":
		dir := os.Getenv("SLINIT_TEST_DIR")
		install(CompileRestrictSUIDSGID())
		_, err := unix.Open(filepath.Join(dir, "suid"), unix.O_CREAT|unix.O_WRONLY, 0o4755)
		want("open(O_CREAT, 04755)", errnoOf(err), unix.EPERM)
		_, err = unix.Openat(unix.AT_FDCWD, filepath.Join(dir, "sgid"), unix.O_CREAT|unix.O_WRONLY, 0o2755)
		want("openat(O_CREAT, 02755)", errnoOf(err), unix.EPERM)
		want("mkdir(02775)", errnoOf(unix.Mkdir(filepath.Join(dir, "d"), 0o2775)), unix.EPERM)
		if fd, err := unix.Open(filepath.Join(dir, "plain"), unix.O_CREAT|unix.O_WRONLY, 0o644); err != nil {
			fail("open(O_CREAT, 0644): %v", err)
		} else {
			unix.Close(fd)
		}
		how := unix.OpenHow{Flags: unix.O_RDONLY}
		atFDCWD := unix.AT_FDCWD
		path, _ := unix.BytePtrFromString(dir)
		_, _, e := unix.Syscall6(unix.SYS_OPENAT2, uintptr(atFDCWD),
			uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
		want("openat2", e, unix.ENOSYS)
	case "file-systems":
		// Run as root of a user and mount namespace of its own, where
		// every mount call works until the filter refuses it.
		dir := os.Getenv("SLINIT_TEST_DIR")
		if err := unix.Mount("tmpfs", dir, "tmpfs", 0, ""); err != nil {
			if err == unix.EPERM || err == unix.EACCES {
				// The namespace exists but grants no capabilities:
				// Ubuntu 24.04's apparmor_restrict_unprivileged_userns
				// does exactly that. Nothing to test here.
				fmt.Fprintf(os.Stderr, "set-up mount: %v\n", err)
				os.Exit(restrictChildSkip)
			}
			fail("set-up mount: %v", err)
		}
		if err := unix.Mount("", dir, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
			fail("set-up remount ro: %v", err)
		}
		install(CompileRestrictFileSystems())
		want("mount(MS_REMOUNT)", errnoOf(unix.Mount("", dir, "", unix.MS_REMOUNT|unix.MS_BIND, "")), unix.EPERM)
		attr := unix.MountAttr{Attr_clr: unix.MOUNT_ATTR_RDONLY}
		want("mount_setattr(clear RDONLY)", errnoOf(unix.MountSetattr(-1, dir, 0, &attr)), unix.EPERM)
		atFDCWD := unix.AT_FDCWD
		path, _ := unix.BytePtrFromString(dir)
		_, _, e := unix.Syscall6(unix.SYS_OPEN_TREE_ATTR, uintptr(atFDCWD),
			uintptr(unsafe.Pointer(path)), unix.OPEN_TREE_CLONE, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
		want("open_tree_attr(OPEN_TREE_CLONE)", e, unix.EPERM)
		want("pivot_root", errnoOf(unix.PivotRoot(dir, dir)), unix.EPERM)
	default:
		fail("unknown case %q", c)
	}
	os.Exit(0)
}

func errnoOf(err error) unix.Errno {
	if e, ok := err.(unix.Errno); ok {
		return e
	}
	return 0
}

func runRestrictChild(t *testing.T, name string, env ...string) {
	t.Helper()
	runRestrictChildAttr(t, name, nil, env...)
}

func runRestrictChildAttr(t *testing.T, name string, attr *syscall.SysProcAttr, env ...string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append(os.Environ(), restrictChildEnv+"="+name), env...)
	cmd.SysProcAttr = attr
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if attr != nil && cmd.Process == nil {
		t.Skipf("%s: cannot start the child: %v", name, err)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ee.ExitCode() == restrictChildSkip {
			t.Skipf("%s: the user namespace grants no capabilities here: %s", name, out)
		}
		if ws := ee.Sys().(syscall.WaitStatus); ws.Signaled() {
			t.Fatalf("%s: child killed by %v — a refused call must fail, not kill", name, ws.Signal())
		}
	}
	t.Fatalf("%s: %v\n%s", name, err, out)
}

func TestRestrictNamespacesOnKernel(t *testing.T) {
	runRestrictChild(t, "namespaces")
	if py, err := exec.LookPath("python3"); err == nil {
		runRestrictChild(t, "namespaces", "SLINIT_TEST_PYTHON="+py)
	}
}

func TestRestrictAddressFamiliesOnKernel(t *testing.T) {
	runRestrictChild(t, "address-families")
}

func TestRestrictSUIDSGIDOnKernel(t *testing.T) {
	dir := t.TempDir()
	runRestrictChild(t, "suid", "SLINIT_TEST_DIR="+dir)
	for _, n := range []string{"suid", "sgid", "d"} {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s was created despite the filter", n)
		}
	}
}

// restrict-file-systems must refuse every call that mounts or changes a
// mount. mount_setattr, open_tree_attr and pivot_root were missing, so
// mount_setattr could clear MS_RDONLY from a read-only mount.
func TestRestrictFileSystemsOnKernel(t *testing.T) {
	runRestrictChildAttr(t, "file-systems", &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}, "SLINIT_TEST_DIR="+t.TempDir())
}

// Every syscall a restrict-* rule names must resolve on amd64: an unknown
// name fails the compile, so a typo here would refuse to start every
// service using that knob.
func TestRestrictRuleNamesResolve(t *testing.T) {
	if NativeArch() != "x86-64" {
		t.Skip("names checked against the amd64 table")
	}
	var names []string
	for _, rs := range [][]rule{realtimeRules, namespaceRules, suidSGIDRules, fileSystemRules} {
		for _, r := range rs {
			names = append(names, r.syscall)
		}
	}
	for _, r := range realtimeExcepts {
		names = append(names, r.syscall)
	}
	names = append(names, "personality", "socket")
	for _, n := range names {
		if _, ok := SyscallNumber(n); !ok {
			t.Errorf("syscall %q does not resolve", n)
		}
	}
}

// restrict-file-systems covers systemd's @mount group (but chroot, which
// mounts nothing): a mount call systemd adds to the group and the list
// lacks would be left open, as mount_setattr was.
func TestRestrictFileSystemsCoversMountGroup(t *testing.T) {
	have := map[string]bool{}
	for _, r := range fileSystemRules {
		have[r.syscall] = true
	}
	for _, n := range syscallGroups["@mount"] {
		if n != "chroot" && !have[n] {
			t.Errorf("@mount member %q is not refused by restrict-file-systems", n)
		}
	}
}
