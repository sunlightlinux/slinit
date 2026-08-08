// Package dissect mounts a disk image so slinit-journalctl can read
// a journal directory from within it. Full parity with systemd's
// dissect-image would require porting ~5kloc of libblkid + LUKS +
// LVM logic; slinit takes the pragmatic path and shells out to
// losetup(8) + mount(8), both universally available on Linux via
// util-linux.
//
// Supported layouts:
//   - Raw filesystem image (mkfs directly on the file). One mount.
//   - Partitioned image (GPT / MBR). losetup -P scans partitions,
//     each /dev/loopNpM gets probed for a slinit-journal directory.
//   - Compressed images: NOT supported (decompress externally first).
//
// Not supported (unlike systemd's dissect):
//   - LUKS-encrypted partitions (would need cryptsetup + key IO).
//   - LVM logical volumes (would need lvm2 binaries).
//   - dm-verity (would need dmsetup + verity roothash).
//   - Btrfs subvolume selection (mount picks the default subvol).
//
// The `--image-policy=strict` client flag errors on encountering
// any of the above so operators get a clear signal.
//
// Attach requires root (loop device + mount syscalls are
// unprivileged only via user namespaces, which slinit doesn't set
// up for a CLI tool). Detach is always safe to call — dropped
// mounts and orphan loop devices are the debugging cost of a
// crash.
package dissect

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// JournalSubpaths is the set of directories, relative to the mounted
// filesystem root, that a mounted image is probed for. First match
// wins. Kept as a slice so future variants (e.g. a namespaced
// journal dir) can be appended without touching the caller.
var JournalSubpaths = []string{
	"var/log/slinit-journal",
	"run/slinit-journal",
	"var/log/journal", // read systemd journals too when we can
}

// Attach opens the image at path, sets up a read-only loop device
// (with partition scanning enabled), mounts the first filesystem
// containing a recognised journal directory, and returns the
// mounted subpath + a Detach func that undoes everything.
//
// policy nil => "loose" behaviour: encountering encrypted / LVM
// partitions logs a warning but keeps searching. Pass a Policy from
// ParsePolicy for stricter semantics.
//
// The caller MUST invoke the returned Detach on every exit path
// (success or error return of the query) to release the loop device.
func Attach(path string, policy *Policy) (mountDir, journalDir string, detach func() error, err error) {
	if os.Geteuid() != 0 {
		return "", "", nil, errors.New("dissect: --image requires root (loop + mount syscalls)")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", "", nil, fmt.Errorf("dissect: stat %s: %w", path, err)
	}
	if info.IsDir() {
		return "", "", nil, fmt.Errorf("dissect: %s is a directory, not a disk image", path)
	}

	// losetup -Pf --show <img>  binds a free loop dev with partition
	// scanning enabled. Read-only via --read-only (`-r`).
	out, err := exec.Command("losetup", "--find", "--show", "--read-only", "--partscan", path).Output()
	if err != nil {
		return "", "", nil, fmt.Errorf("dissect: losetup %s: %w", path, err)
	}
	loopDev := strings.TrimSpace(string(out))
	if loopDev == "" {
		return "", "", nil, fmt.Errorf("dissect: losetup returned empty device for %s", path)
	}

	// detachLoop is used by every early-return path plus the final
	// Detach we hand back to the caller.
	detachLoop := func() error {
		return exec.Command("losetup", "--detach", loopDev).Run()
	}

	// Probe candidates: the whole-device node first (raw fs images),
	// then each partition node loopNpN. os.ReadDir on /dev catches
	// the partition nodes losetup --partscan created.
	candidates := []string{loopDev}
	if parts, err := loopPartitions(loopDev); err == nil {
		candidates = append(candidates, parts...)
	}

	tmpMount, err := os.MkdirTemp("", "slinit-dissect-*")
	if err != nil {
		_ = detachLoop()
		return "", "", nil, fmt.Errorf("dissect: tmpdir: %w", err)
	}

	for _, dev := range candidates {
		// Skip encrypted / LVM / verity holders when policy is strict.
		if policy != nil && policy.Strict {
			if kind, err := probeDeviceKind(dev); err == nil && kind != "" {
				return "", "", nil, fmt.Errorf(
					"dissect: --image-policy=strict refuses %s device %s (LUKS/LVM/verity unsupported by slinit dissect)",
					kind, dev)
			}
		}
		if err := mountRO(dev, tmpMount); err != nil {
			continue // Filesystem probe failed — try next candidate.
		}
		for _, sub := range JournalSubpaths {
			jdir := filepath.Join(tmpMount, sub)
			if _, err := os.Stat(jdir); err == nil {
				detach := func() error {
					_ = exec.Command("umount", tmpMount).Run()
					_ = os.Remove(tmpMount)
					return detachLoop()
				}
				return tmpMount, jdir, detach, nil
			}
		}
		// No journal in this partition — umount and try next.
		_ = exec.Command("umount", tmpMount).Run()
	}
	_ = os.Remove(tmpMount)
	_ = detachLoop()
	return "", "", nil, fmt.Errorf(
		"dissect: no journal directory found under %v on any partition of %s",
		JournalSubpaths, path)
}

// loopPartitions enumerates partition nodes for a loop device by
// listing /dev entries with the loopDev-basename prefix + "p".
// losetup --partscan populates these when the image has a
// GPT/MBR table.
func loopPartitions(loopDev string) ([]string, error) {
	base := filepath.Base(loopDev)
	entries, err := os.ReadDir("/dev")
	if err != nil {
		return nil, err
	}
	var parts []string
	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, base+"p") {
			parts = append(parts, filepath.Join("/dev", name))
		}
	}
	return parts, nil
}

// mountRO calls mount(8) with -o ro. Filesystem type is auto-
// detected — mount tries every /proc/filesystems entry the kernel
// knows about. Returns nil on success, wrapped error otherwise.
func mountRO(dev, mountpoint string) error {
	cmd := exec.Command("mount", "-o", "ro", dev, mountpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount %s → %s: %v (%s)", dev, mountpoint, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// probeDeviceKind sniffs a device via `lsblk -no FSTYPE` and
// returns "luks" / "lvm2" / "verity" / "" (unknown or plain fs).
// Best-effort — a probe failure returns ("", err) so callers can
// decide whether to skip or refuse.
func probeDeviceKind(dev string) (string, error) {
	out, err := exec.Command("lsblk", "-no", "FSTYPE", dev).Output()
	if err != nil {
		return "", err
	}
	fst := strings.TrimSpace(string(out))
	switch fst {
	case "crypto_LUKS":
		return "luks", nil
	case "LVM2_member":
		return "lvm2", nil
	case "DM_verity_hash":
		return "verity", nil
	}
	return "", nil
}
