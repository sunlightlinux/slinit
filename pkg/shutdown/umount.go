package shutdown

import (
	"bufio"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

// mountEntry represents a single row parsed from /proc/mounts.
type mountEntry struct {
	source string
	target string
	fstype string
	opts   string
}

// unmountProcPath and swapsProcPath are overridable for tests.
var (
	unmountProcPath = "/proc/mounts"
	swapsProcPath   = "/proc/swaps"
)

// mockable syscalls for umount/swapoff tests.
var (
	unmountFunc = syscall.Unmount
	mountFunc   = unix.Mount
	swapoffFunc = swapoffSyscall
)

// swapoffSyscall invokes swapoff(2) directly. golang.org/x/sys/unix does
// not wrap this syscall on Linux, so we call it through SYS_SWAPOFF.
func swapoffSyscall(path string) error {
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(unix.SYS_SWAPOFF, uintptr(unsafe.Pointer(p)), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// readMounts parses /proc/mounts (or a compatible file) and returns its
// entries in the order they appear.
func readMounts(path string) ([]mountEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseMounts(f)
}

func parseMounts(r io.Reader) ([]mountEntry, error) {
	var entries []mountEntry
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		e := mountEntry{
			source: unescapeMount(fields[0]),
			target: unescapeMount(fields[1]),
			fstype: fields[2],
		}
		if len(fields) >= 4 {
			e.opts = fields[3]
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// unescapeMount decodes \NNN octal escapes used by the kernel when writing
// paths to /proc/mounts (e.g. \040 for space, \011 for tab).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			v := 0
			ok := true
			for j := 1; j <= 3; j++ {
				c := s[i+j]
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				sb.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		sb.WriteByte(s[i])
	}
	return sb.String()
}

// sortMountsReverse orders entries so that children unmount before their
// parents. Deeper paths come first; ties are broken lexicographically.
func sortMountsReverse(entries []mountEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		li, lj := len(entries[i].target), len(entries[j].target)
		if li != lj {
			return li > lj
		}
		return entries[i].target > entries[j].target
	})
}

// shouldSkipUnmount returns true for mount points that must remain
// mounted during shutdown. Root stays — we remount it read-only as the
// final step so journals don't need replay on the next boot.
func shouldSkipUnmount(e mountEntry) bool {
	return e.target == "/"
}

// unmountAll reads /proc/mounts, sorts it deepest-first, and unmounts
// every entry except root. Busy mounts fall back to MNT_DETACH; remaining
// failures are remounted read-only. Finally root itself is remounted
// read-only. Replaces the previous exec of /bin/umount -a -r.
func unmountAll(logger *logging.Logger) {
	logger.Info("Unmounting filesystems...")

	entries, err := readMounts(unmountProcPath)
	if err != nil {
		logger.Debug("Cannot read %s: %v", unmountProcPath, err)
		return
	}

	sortMountsReverse(entries)

	for _, e := range entries {
		if shouldSkipUnmount(e) {
			continue
		}
		unmountBounded(e, logger)
	}

	// Final step: remount / read-only so a dirty shutdown doesn't force
	// fsck on next boot.
	if err := mountFunc("", "/", "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		logger.Debug("remount / ro: %v", err)
	} else {
		logger.Debug("Root filesystem remounted read-only")
	}
}

// umountTimeout bounds one mount's unmount, as systemd's shutdown bounds
// each umount2 with DEFAULT_TIMEOUT_USEC.
var umountTimeout = 90 * time.Second

// unmountBounded is unmountOne with a time limit. An unmount can block
// for good — a dead NFS or CIFS server, a FUSE daemon that is gone — and
// this runs after every process is dead, with nothing left to rescue the
// shutdown. Past umountTimeout the mount is detached instead and the
// stuck call is left behind.
func unmountBounded(e mountEntry, logger *logging.Logger) {
	done := make(chan struct{})
	go func() {
		unmountOne(e, logger)
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(umountTimeout):
	}
	logger.Error("Unmounting %s timed out after %v, detaching it", e.target, umountTimeout)
	detached := make(chan error, 1)
	go func() { detached <- unmountFunc(e.target, int(unix.MNT_DETACH)) }()
	select {
	case err := <-detached:
		if err != nil {
			logger.Debug("Lazy unmount of %s: %v", e.target, err)
		}
	case <-time.After(umountTimeout):
		logger.Error("Detaching %s timed out too, leaving it", e.target)
	}
}

// isNetworkFS reports a filesystem whose server may be gone at shutdown:
// systemd's @network set plus the FUSE network filesystems it names.
func isNetworkFS(fstype string) bool {
	fstype = strings.TrimPrefix(fstype, "fuse.")
	switch fstype {
	case "afs", "ceph", "cifs", "gfs", "gfs2", "ncp", "ncpfs", "nfs", "nfs4",
		"ocfs2", "orangefs", "pvfs2", "smb3", "smbfs",
		"davfs", "glusterfs", "lustre", "sshfs":
		return true
	}
	return false
}

func unmountOne(e mountEntry, logger *logging.Logger) {
	// Clean unmount first. For a network filesystem that is MNT_FORCE,
	// which aborts requests pending on a dead server instead of waiting
	// on them (systemd's shutdown forces every non-lazy unmount).
	flags := 0
	if isNetworkFS(e.fstype) {
		flags = unix.MNT_FORCE
	}
	err := unmountFunc(e.target, flags)
	if err == nil {
		logger.Debug("Unmounted %s", e.target)
		return
	}
	if err == syscall.EINVAL || err == syscall.ENOENT {
		// Not mounted anymore — probably raced with a lazy parent unmount.
		return
	}

	// Busy → lazy detach.
	if err := unmountFunc(e.target, int(unix.MNT_DETACH)); err == nil {
		logger.Debug("Lazy-unmounted %s", e.target)
		return
	}

	// Last resort: remount read-only so pending writes are flushed and
	// the filesystem is safe to abandon.
	if err := mountFunc("", e.target, "", unix.MS_REMOUNT|unix.MS_RDONLY, ""); err != nil {
		logger.Debug("Failed to clean up %s: %v", e.target, err)
	} else {
		logger.Debug("Remounted %s read-only", e.target)
	}
}

// swapOff reads /proc/swaps and disables each swap device via the
// swapoff(2) syscall. Replaces the previous exec of /sbin/swapoff -a.
func swapOff(logger *logging.Logger) {
	logger.Info("Disabling swap...")

	devs, err := readSwaps(swapsProcPath)
	if err != nil {
		logger.Debug("Cannot read %s: %v", swapsProcPath, err)
		return
	}

	for _, dev := range devs {
		if err := swapoffFunc(dev); err != nil {
			logger.Debug("swapoff(%s): %v", dev, err)
		} else {
			logger.Debug("Swap disabled: %s", dev)
		}
	}
}

// readSwaps parses /proc/swaps and returns the device path of every
// active swap area. The first line (header) is skipped.
func readSwaps(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseSwaps(f)
}

func parseSwaps(r io.Reader) ([]string, error) {
	var devices []string
	scanner := bufio.NewScanner(r)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		devices = append(devices, unescapeMount(fields[0]))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return devices, nil
}
