package switchroot

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// TestDo_EndToEnd runs the whole switch-root as PID 1 of a fresh
// user+pid+mount namespace: a tmpfs stands in for the initramfs, with a
// separate tmpfs mounted at its /run, and a second tmpfs is NEWROOT. The
// mounts, the pivot, the kill sweep and the cleanup are the real ones;
// only the final exec is recorded instead of performed, and it checks
// what the new init would see. Do had only ever been tested in pieces —
// PID 1 kept the sequence itself out of reach.
func TestDo_EndToEnd(t *testing.T) {
	if os.Getenv("SLINIT_SWITCHROOT_DO_CHILD") == "1" {
		doChild()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDo_EndToEnd$")
	cmd.Env = append(os.Environ(), "SLINIT_SWITCHROOT_DO_CHILD=1", "SLINIT_SWITCHROOT_DIR="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS | syscall.CLONE_NEWPID,
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
	if !strings.Contains(string(out), "DO-OK") {
		t.Fatalf("child did not finish:\n%s", out)
	}
}

func doChild() {
	fail := func(f string, a ...any) {
		os.Stdout.WriteString("FAIL: " + fmt.Sprintf(f, a...) + "\n")
		os.Exit(1)
	}
	skip := func(why string, err error) {
		os.Stdout.WriteString("SKIP: " + why + ": " + err.Error() + "\n")
		os.Exit(0)
	}
	// Do sends kill(-1). Only ever as PID 1 of this namespace, where that
	// reaches nothing but the namespace's own processes.
	if os.Getpid() != 1 {
		skip("not PID 1", errors.New("no pid namespace"))
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		skip("make-private", err)
	}
	old := os.Getenv("SLINIT_SWITCHROOT_DIR")
	if err := unix.Mount("tmpfs", old, "tmpfs", 0, ""); err != nil {
		skip("mount tmpfs", err)
	}
	for _, d := range []string{"dev", "proc", "sys", "run", "etc", "newroot"} {
		if err := os.MkdirAll(filepath.Join(old, d), 0o755); err != nil {
			fail("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(old, "etc/initrd-junk"), []byte("x"), 0o644); err != nil {
		fail("write junk: %v", err)
	}
	// /run is a mount of its own, as in an initramfs; /dev, /proc and /sys
	// are plain directories here and must be left where they are.
	if err := unix.Mount("tmpfs", filepath.Join(old, "run"), "tmpfs", 0, ""); err != nil {
		fail("mount run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(old, "run/marker"), []byte("x"), 0o644); err != nil {
		fail("write run marker: %v", err)
	}
	newroot := filepath.Join(old, "newroot")
	if err := unix.Mount("tmpfs", newroot, "tmpfs", 0, ""); err != nil {
		fail("mount newroot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(newroot, "sbin"), 0o755); err != nil {
		fail("mkdir sbin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newroot, "sbin/init"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		fail("write init: %v", err)
	}

	// A process that ignores SIGTERM: the SIGKILL sweep has to end it. It
	// says when the trap is in place; a SIGTERM before that would kill it
	// and let a missing SIGKILL sweep pass unnoticed.
	ready := filepath.Join(os.TempDir(), "holdout-ready")
	holdout := exec.Command("/bin/sh", "-c",
		"trap '' TERM; : > "+ready+"; while :; do sleep 1; done")
	if err := holdout.Start(); err != nil {
		fail("start holdout: %v", err)
	}
	holdoutPid := holdout.Process.Pid
	for i := 0; ; i++ {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if i == 500 {
			fail("holdout never got its trap in place")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := unix.Chroot(old); err != nil {
		fail("chroot: %v", err)
	}
	if err := os.Chdir("/"); err != nil {
		fail("chdir: %v", err)
	}

	var steps []string
	step := func(s string) { steps = append(steps, s) }
	hooksRunFunc = func(point string, _ *logging.Logger) (int, int) {
		step("hooks:" + point)
		return 0, 0
	}
	realKill := killAllFunc
	killAllFunc = func(sig unix.Signal) { step("kill:" + sig.String()); realKill(sig) }
	sleepFunc = func(d time.Duration) { step("sleep"); time.Sleep(d) }
	realReap := reapFunc
	reapFunc = func() { step("reap"); realReap() }
	realRemove := removeAllFunc
	removeAllFunc = func(l *logging.Logger, nr string) { step("cleanup"); realRemove(l, nr) }
	realPivot := pivotChrootFunc
	pivotChrootFunc = func(nr string) error { step("pivot"); return realPivot(nr) }

	var execArgv []string
	var seen []string
	execFunc = func(argv0 string, argv []string, env []string) error {
		step("exec")
		execArgv = append([]string{argv0}, argv...)
		for _, p := range []string{"/sbin/init", "/run/marker", "/etc/initrd-junk"} {
			if _, err := os.Stat(p); err == nil {
				seen = append(seen, p)
			}
		}
		return errors.New("recorded")
	}

	err := Do("/newroot", "", logging.New(logging.LevelError), func() { step("stop-services") })
	if err == nil || !strings.Contains(err.Error(), "exec /sbin/init: recorded") {
		fail("Do returned %v, want the recorded exec error", err)
	}

	want := []string{"hooks:switch-root", "stop-services", "kill:terminated", "sleep",
		"kill:killed", "reap", "cleanup", "pivot", "exec"}
	if strings.Join(steps, " ") != strings.Join(want, " ") {
		fail("steps %v, want %v", steps, want)
	}
	if strings.Join(execArgv, " ") != "/sbin/init /sbin/init" {
		fail("exec argv %v, want /sbin/init with argv[0] /sbin/init", execArgv)
	}
	// After the pivot / is the new root: its init is there, /run came
	// along with its contents, and nothing of the initramfs is visible.
	if strings.Join(seen, " ") != "/sbin/init /run/marker" {
		fail("new init sees %v, want /sbin/init and /run/marker only", seen)
	}
	if err := unix.Kill(holdoutPid, 0); !errors.Is(err, unix.ESRCH) {
		fail("SIGTERM-ignoring process %d survived the sweep (kill 0: %v)", holdoutPid, err)
	}
	os.Stdout.WriteString("DO-OK\n")
	os.Exit(0)
}
