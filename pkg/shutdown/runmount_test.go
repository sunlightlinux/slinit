package shutdown

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// btrfsSubvolCreate is BTRFS_IOC_SUBVOL_CREATE, _IOW(0x94, 14, struct
// btrfs_ioctl_vol_args) with the struct's 4096 bytes.
const btrfsSubvolCreate = 0x5000940e

// createSubvol makes name a btrfs subvolume under dir, which needs no
// privilege.
func createSubvol(dir, name string) error {
	var args struct {
		fd   int64
		name [4088]byte
	}
	copy(args.name[:], name)
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, d.Fd(), btrfsSubvolCreate, uintptr(unsafe.Pointer(&args)))
	if errno != 0 {
		return errno
	}
	return nil
}

// A /run that is a plain directory on its own btrfs subvolume has an
// st_dev of its own, which isMountpoint took for a mount: no tmpfs went
// on it, and /run lived on disk, across reboots.
func TestStageRunOnBtrfsSubvolume(t *testing.T) {
	if !inPrivateNS(t) {
		return
	}
	base := os.Getenv("SLINIT_TEST_BTRFS_DIR")
	if base == "" {
		base = "/var/tmp"
	}
	var sfs unix.Statfs_t
	if err := unix.Statfs(base, &sfs); err != nil || sfs.Type != unix.BTRFS_SUPER_MAGIC {
		t.Skipf("%s is not btrfs (set SLINIT_TEST_BTRFS_DIR)", base)
	}
	dir, err := os.MkdirTemp(base, "slinit-runsubvol-")
	if err != nil {
		t.Skipf("mkdtemp under %s: %v", base, err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := createSubvol(dir, "run"); err != nil {
		t.Skipf("create subvolume: %v", err)
	}
	run := filepath.Join(dir, "run")
	t.Cleanup(func() {
		unix.Unmount(run, unix.MNT_DETACH)
		os.Remove(run) // an empty subvolume goes with rmdir
	})

	stageRunAt(run, RunModeMount, logging.New(logging.LevelError))

	if err := unix.Statfs(run, &sfs); err != nil {
		t.Fatal(err)
	}
	if sfs.Type != unix.TMPFS_MAGIC {
		t.Errorf("/run on a btrfs subvolume: filesystem %#x after staging, want tmpfs (%d mounts there)",
			sfs.Type, countMounts(t, run))
	}
}

// keepMountedRun's decision, with the kernel's answers injected.
func TestKeepMountedRunDecision(t *testing.T) {
	errNoStatx := errors.New("no statx")
	cases := []struct {
		name     string
		root     bool
		statxErr error
		inInfo   bool
		magic    int64
		want     bool
	}{
		{"tmpfs mount", true, nil, true, unix.TMPFS_MAGIC, true},
		{"ramfs mount", true, nil, true, unix.RAMFS_MAGIC, true},
		{"subvolume, not a mount", false, nil, false, unix.BTRFS_SUPER_MAGIC, false},
		{"mounted, but on disk", true, nil, true, unix.BTRFS_SUPER_MAGIC, false},
		{"no statx, in mountinfo", false, errNoStatx, true, unix.TMPFS_MAGIC, true},
		{"no statx, not in mountinfo", false, errNoStatx, false, unix.TMPFS_MAGIC, false},
	}
	for _, c := range cases {
		p := runProbe{
			mountRoot:   func(string) (bool, error) { return c.root, c.statxErr },
			inMountinfo: func(string) bool { return c.inInfo },
			fsMagic:     func(string) (int64, error) { return c.magic, nil },
		}
		if got := p.keepMountedRun("/run"); got != c.want {
			t.Errorf("%s: keep = %v, want %v", c.name, got, c.want)
		}
	}
}
