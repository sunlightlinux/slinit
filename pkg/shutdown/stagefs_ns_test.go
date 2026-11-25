package shutdown

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// inPrivateNS re-runs the calling test as root of a new user, PID,
// mount and network namespace, where it may mount. It returns true in the re-run,
// where the test goes on, and false in the caller, which then returns.
func inPrivateNS(t *testing.T) bool {
	t.Helper()
	if os.Getenv("SLINIT_TEST_PRIVATENS") != "" {
		if os.Geteuid() != 0 {
			t.Skip("not root inside the namespace")
		}
		return true
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not available")
	}
	if exec.Command("unshare", "-Urpfmn", "--mount-proc", "true").Run() != nil {
		t.Skip("unshare -Urpfmn not permitted here")
	}
	cmd := exec.Command("unshare", "-Urpfmn", "--mount-proc",
		os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "SLINIT_TEST_PRIVATENS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("in namespace: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "--- SKIP") {
		t.Skipf("skipped in namespace:\n%s", out)
	}
	return false
}

func countMounts(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 4 && f[4] == path {
			n++
		}
	}
	return n
}

// --run-mode=mount is documented as a no-op on a /run that is already a
// mountpoint, but mounted a fresh tmpfs over it, hiding the initramfs's
// /run. remount still empties it; keep leaves it.
func TestStageRunModes(t *testing.T) {
	if !inPrivateNS(t) {
		return
	}
	logger := logging.New(logging.LevelError)
	base := t.TempDir()
	if out, err := exec.Command("mount", "-t", "tmpfs", "none", base).CombinedOutput(); err != nil {
		t.Fatalf("mount tmpfs %s: %v\n%s", base, err, out)
	}
	t.Cleanup(func() { exec.Command("umount", "-l", base).Run() })

	mounted := func(name string) string {
		p := filepath.Join(base, name)
		os.Mkdir(p, 0755)
		if out, err := exec.Command("mount", "-t", "tmpfs", "none", p).CombinedOutput(); err != nil {
			t.Fatalf("mount tmpfs %s: %v\n%s", p, err, out)
		}
		os.WriteFile(filepath.Join(p, "marker"), []byte("x"), 0644)
		return p
	}
	hasMarker := func(p string) bool {
		_, err := os.Stat(filepath.Join(p, "marker"))
		return err == nil
	}

	p := mounted("mount")
	stageRunAt(p, RunModeMount, logger)
	if !hasMarker(p) || countMounts(t, p) != 1 {
		t.Errorf("mount on a mountpoint: marker kept = %v, mounts = %d; want true, 1",
			hasMarker(p), countMounts(t, p))
	}

	plain := filepath.Join(base, "plain")
	os.Mkdir(plain, 0755)
	stageRunAt(plain, RunModeMount, logger)
	if countMounts(t, plain) != 1 {
		t.Errorf("mount on a plain directory: %d mounts; want 1", countMounts(t, plain))
	}

	p = mounted("remount")
	stageRunAt(p, RunModeRemount, logger)
	if hasMarker(p) || countMounts(t, p) != 1 {
		t.Errorf("remount: marker kept = %v, mounts = %d; want false, 1",
			hasMarker(p), countMounts(t, p))
	}

	p = mounted("keep")
	stageRunAt(p, RunModeKeep, logger)
	if !hasMarker(p) || countMounts(t, p) != 1 {
		t.Errorf("keep: marker kept = %v, mounts = %d; want true, 1",
			hasMarker(p), countMounts(t, p))
	}
}

// /proc was mounted at every boot without looking: each boot, and each
// soft reboot, stacked another procfs (3, 4, 5, 6 measured), and the one
// on top lacked the hidepid set on the first. /sys likewise.
func TestMountUnlessTypeDoesNotStack(t *testing.T) {
	if !inPrivateNS(t) {
		return
	}
	logger := logging.New(logging.LevelError)
	for _, fs := range []struct {
		fstype, path string
		magic        int64
	}{
		{"proc", "/proc", unix.PROC_SUPER_MAGIC},
		{"sysfs", "/sys", unix.SYSFS_MAGIC},
	} {
		before := countMounts(t, fs.path)
		mountUnlessType(fs.fstype, fs.path, fs.magic, logger)
		if got := countMounts(t, fs.path); got != before {
			t.Errorf("%s: %d mounts on %s after, %d before", fs.fstype, got, fs.path, before)
		}
	}

	base := t.TempDir()
	if out, err := exec.Command("mount", "-t", "tmpfs", "none", base).CombinedOutput(); err != nil {
		t.Fatalf("mount tmpfs %s: %v\n%s", base, err, out)
	}
	t.Cleanup(func() { exec.Command("umount", "-l", base).Run() })
	p := filepath.Join(base, "proc")
	mountUnlessType("proc", p, unix.PROC_SUPER_MAGIC, logger)
	mountUnlessType("proc", p, unix.PROC_SUPER_MAGIC, logger)
	if got := countMounts(t, p); got != 1 {
		t.Errorf("proc on a plain directory, twice: %d mounts; want 1", got)
	}
}
