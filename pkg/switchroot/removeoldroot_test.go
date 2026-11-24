package switchroot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// TestRemoveOldRoot_NestedNewroot runs the real initramfs cleanup
// inside a user+mount namespace: a tmpfs stands in for the initramfs
// and a second tmpfs is mounted at its /mnt/root as NEWROOT — a
// nested target Precheck accepts. The cleanup must free the old
// root's own files without touching anything on the new root's
// device. It used to RemoveAll every top-level entry except the
// exact name "mnt/root", so /mnt went, and the real root with it.
func TestRemoveOldRoot_NestedNewroot(t *testing.T) {
	if os.Getenv("SLINIT_SWITCHROOT_CHILD") == "1" {
		removeOldRootChild()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRemoveOldRoot_NestedNewroot$")
	cmd.Env = append(os.Environ(), "SLINIT_SWITCHROOT_CHILD=1", "SLINIT_SWITCHROOT_DIR="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
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
}

// removeOldRootChild runs as uid 0 of a fresh user namespace. It
// reports failures on stdout and exits non-zero; t is unavailable.
func removeOldRootChild() {
	fail := func(f string, a ...any) {
		os.Stdout.WriteString("FAIL: " + fmt.Sprintf(f, a...) + "\n")
		os.Exit(1)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		os.Stdout.WriteString("SKIP: make-private: " + err.Error() + "\n")
		os.Exit(0)
	}
	old := os.Getenv("SLINIT_SWITCHROOT_DIR")
	if err := unix.Mount("tmpfs", old, "tmpfs", 0, ""); err != nil {
		os.Stdout.WriteString("SKIP: mount tmpfs: " + err.Error() + "\n")
		os.Exit(0)
	}
	for _, d := range []string{"etc", "mnt/root", "mnt/other", "bin"} {
		if err := os.MkdirAll(filepath.Join(old, d), 0o755); err != nil {
			fail("mkdir %s: %v", d, err)
		}
	}
	for _, f := range []string{"etc/initrd-junk", "mnt/other/junk", "bin/busybox"} {
		if err := os.WriteFile(filepath.Join(old, f), []byte("x"), 0o644); err != nil {
			fail("write %s: %v", f, err)
		}
	}
	if err := os.Symlink("/mnt/root/etc", filepath.Join(old, "etclink")); err != nil {
		fail("symlink: %v", err)
	}
	newroot := filepath.Join(old, "mnt/root")
	if err := unix.Mount("tmpfs", newroot, "tmpfs", 0, ""); err != nil {
		fail("mount newroot: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(newroot, "etc"), 0o755); err != nil {
		fail("mkdir newroot/etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newroot, "etc/precious"), []byte("x"), 0o644); err != nil {
		fail("write precious: %v", err)
	}
	if err := unix.Chroot(old); err != nil {
		fail("chroot: %v", err)
	}
	if err := os.Chdir("/"); err != nil {
		fail("chdir: %v", err)
	}

	defaultRemoveOldRoot(logging.New(logging.LevelError), "/mnt/root")

	if _, err := os.Stat("/mnt/root/etc/precious"); err != nil {
		fail("new root's file was deleted: %v", err)
	}
	for _, gone := range []string{"/etc", "/bin", "/mnt/other", "/etclink"} {
		if _, err := os.Lstat(gone); err == nil {
			fail("old-root entry %s survived the cleanup", gone)
		}
	}
	os.Exit(0)
}
