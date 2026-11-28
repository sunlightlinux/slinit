package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// private-devices (systemd's PrivateDevices=): the service gets a /dev
// of its own, a tmpfs holding only the API pseudo devices (null, zero,
// full, random, urandom, tty), the pseudo-TTY subsystem, /dev/shm,
// /dev/mqueue and /dev/hugepages, the log socket and the usual
// /dev/fd and /dev/std* links, and no physical device, as systemd's
// mount_private_dev builds it. The nodes are bind mounts of the host's,
// so no mknod is needed (and a user namespace allows it). The tmpfs is
// nosuid and noexec, as systemd mounts it. CAP_MKNOD and CAP_SYS_RAWIO
// leave the bounding set (dropPrivateDevCaps), as systemd drops them;
// systemd's cgroup device filter, which slinit has not, is what an
// already-open device would still be subject to.

var privateDevNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

var privateDevDirs = []string{"pts", "shm", "mqueue", "hugepages"}

func mountPrivateDev() error {
	staging, err := os.MkdirTemp("/run", ".slinit-dev-")
	if err != nil {
		if staging, err = os.MkdirTemp("", ".slinit-dev-"); err != nil {
			return fmt.Errorf("private-devices: staging directory: %w", err)
		}
	}
	defer os.Remove(staging)
	if err := unix.Mount("tmpfs", staging, "tmpfs",
		unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_STRICTATIME, "mode=0755"); err != nil {
		return fmt.Errorf("private-devices: tmpfs: %w", err)
	}
	moved := false
	defer func() {
		if !moved {
			_ = unix.Unmount(staging, unix.MNT_DETACH)
		}
	}()

	for _, n := range privateDevNodes {
		src := "/dev/" + n
		st, err := os.Stat(src)
		if err != nil || st.Mode()&os.ModeCharDevice == 0 {
			continue // not a device here: systemd skips it too
		}
		dst := filepath.Join(staging, n)
		if err := os.WriteFile(dst, nil, 0o666); err != nil {
			return fmt.Errorf("private-devices: %s: %w", n, err)
		}
		if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("private-devices: bind %s: %w", src, err)
		}
	}
	for _, d := range privateDevDirs {
		src := "/dev/" + d
		if st, err := os.Stat(src); err != nil || !st.IsDir() {
			continue
		}
		dst := filepath.Join(staging, d)
		if err := os.Mkdir(dst, 0o755); err != nil {
			return fmt.Errorf("private-devices: %s: %w", d, err)
		}
		if err := unix.Mount(src, dst, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("private-devices: bind %s: %w", src, err)
		}
	}
	// /dev/ptmx: a link to pts/ptmx where the host has one (containers),
	// else the host's node.
	if fi, err := os.Lstat("/dev/ptmx"); err == nil {
		dst := filepath.Join(staging, "ptmx")
		if fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Symlink("pts/ptmx", dst); err != nil {
				return fmt.Errorf("private-devices: ptmx: %w", err)
			}
		} else if fi.Mode()&os.ModeCharDevice != 0 {
			if err := os.WriteFile(dst, nil, 0o666); err != nil {
				return fmt.Errorf("private-devices: ptmx: %w", err)
			}
			if err := unix.Mount("/dev/ptmx", dst, "", unix.MS_BIND, ""); err != nil {
				return fmt.Errorf("private-devices: bind /dev/ptmx: %w", err)
			}
		}
	}
	// /dev/log, which slinit itself serves: the socket, or what the link
	// points at.
	if fi, err := os.Lstat("/dev/log"); err == nil {
		dst := filepath.Join(staging, "log")
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink("/dev/log"); err == nil {
				_ = os.Symlink(target, dst)
			}
		} else if err := os.WriteFile(dst, nil, 0o666); err == nil {
			_ = unix.Mount("/dev/log", dst, "", unix.MS_BIND, "")
		}
	}
	for name, target := range map[string]string{
		"fd": "/proc/self/fd", "stdin": "/proc/self/fd/0", "stdout": "/proc/self/fd/1", "stderr": "/proc/self/fd/2",
	} {
		if err := os.Symlink(target, filepath.Join(staging, name)); err != nil {
			return fmt.Errorf("private-devices: %s: %w", name, err)
		}
	}

	if err := os.MkdirAll("/dev", 0o755); err != nil {
		return fmt.Errorf("private-devices: /dev: %w", err)
	}
	// Take the host's /dev away underneath first, as systemd unmounts
	// the old tree: stacked on top of it, the private one could be
	// unmounted by a root service, the host's coming back with every
	// device. The binds above are mounts of their own and stay. It fails
	// where /dev is no mount of its own (a container's rootfs), and in a
	// user namespace, where the kernel locks the inherited mount; the
	// private /dev is then stacked on it.
	_ = unix.Unmount("/dev", unix.MNT_DETACH)
	if err := unix.Mount(staging, "/dev", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("private-devices: move onto /dev: %w", err)
	}
	moved = true
	return nil
}

// dropPrivateDevCaps takes CAP_MKNOD and CAP_SYS_RAWIO out of the
// bounding set, as systemd's PrivateDevices= does: no new device node,
// no raw I/O around the private /dev.
func dropPrivateDevCaps() error {
	for _, c := range []uintptr{unix.CAP_MKNOD, unix.CAP_SYS_RAWIO} {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, c, 0, 0, 0); err != nil && err != unix.EINVAL {
			return fmt.Errorf("private-devices: drop capability %d: %w", c, err)
		}
	}
	return nil
}
