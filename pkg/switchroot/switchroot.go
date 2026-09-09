// Package switchroot implements the initramfs → real-root transition
// pattern used by systems that boot slinit as PID 1 inside an
// initramfs. The initramfs does the platform-specific work (decrypt
// LUKS, assemble LVM, wait for networked storage, mount the real
// root at /newroot) and then hands off to slinit-in-newroot by
// calling Do, which:
//
//   1. Stops all initramfs services cleanly.
//   2. Kills any lingering processes (SIGTERM then SIGKILL).
//   3. Moves /dev, /proc, /sys, /run into the new root via MS_MOVE.
//   4. chdir(newroot), mount --move newroot / , chroot("."), chdir(/).
//   5. If the old root was ramfs/tmpfs (initramfs), deletes its
//      contents to free RAM before the exec.
//   6. syscall.Exec(newinit, argv, environ) — PID 1 stays PID 1,
//      the kernel keeps its init reference intact.
//
// finit-parity (Finit `switch_root` in src/initramfs.c). Deliberate
// divergences from finit:
//   - Precheck is a distinct function returning a text reason, so
//     the control-socket handler can relay the reason back to the
//     client BEFORE the point of no return.
//   - Go's *os.File finalizers would fight with the chroot; we use
//     raw syscall.Open / unix.Mount / syscall.Exec exclusively past
//     Precheck to keep the runtime out of the picture.
package switchroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"golang.org/x/sys/unix"
)

const (
	// defaultInit is the executable slinit hands off to when the
	// caller doesn't specify one. Matches systemd + finit.
	defaultInit = "/sbin/init"

	// killSettle is the delay between the SIGTERM sweep and the
	// SIGKILL sweep. 500 ms matches finit + gives most processes
	// enough time to cascade-stop through their signal handlers
	// before we cut them off.
	killSettle = 500 * time.Millisecond
)

// ErrNotPID1 is returned by Precheck when the caller isn't PID 1.
// switch-root is only meaningful from an initramfs-hosted init:
// running it from a normal shell or a container manager would
// disconnect user sessions from the mounts they're watching.
var ErrNotPID1 = errors.New("switch-root: must be PID 1")

// Hookable primitives — all default to the real syscalls. Tests
// override these to exercise the control flow without touching the
// running system.
var (
	// mountFunc mirrors unix.Mount(source, target, fstype, flags, data).
	mountFunc = unix.Mount
	// pivotChrootFunc encapsulates the chdir+mount-move+chroot+chdir
	// dance so tests can validate the sequence without actually
	// chroot()ing the test binary.
	pivotChrootFunc = defaultPivotChroot
	// killAllFunc broadcasts a signal to every non-init process
	// (kill(-1, signo) semantics). Test double records the signal.
	killAllFunc = defaultKillAll
	// reapFunc drains any zombie children with waitpid(-1, WNOHANG)
	// until none remain.
	reapFunc = defaultReap
	// execFunc replaces the current process with newinit. Defaults
	// to syscall.Exec; test double records the args and returns.
	execFunc = syscall.Exec
	// sleepFunc controls the settle between SIGTERM and SIGKILL.
	// Overridable so tests don't wait real time.
	sleepFunc = time.Sleep
	// statfsFunc reads a filesystem's type magic. Used to detect
	// whether the old root is initramfs (RAMFS_MAGIC / TMPFS_MAGIC)
	// so we can free that RAM by deleting the old contents.
	statfsFunc = unix.Statfs
	// removeAllFunc deletes the old-root contents when the old root
	// is initramfs. Best-effort — errors here don't fail the switch;
	// they just cost some RAM until the process re-execs.
	removeAllFunc = defaultRemoveOldRoot
)

// Precheck validates a switch-root request without side effects, so
// the control-socket handler can reject a bad request cleanly
// (RplyBadReq + text reason) before committing to teardown. Returns
// the resolved newinit (with defaulting applied) so the caller and
// Do agree on the path.
func Precheck(newroot, newinit string) (string, error) {
	if newroot == "" {
		return "", errors.New("newroot: empty")
	}
	if !filepath.IsAbs(newroot) {
		return "", fmt.Errorf("newroot: %q not absolute", newroot)
	}
	if newinit == "" {
		newinit = defaultInit
	}
	if !filepath.IsAbs(newinit) {
		return "", fmt.Errorf("newinit: %q not absolute", newinit)
	}
	if os.Getpid() != 1 {
		return newinit, ErrNotPID1
	}
	// Verify newroot is a directory.
	newSt, err := os.Stat(newroot)
	if err != nil {
		return newinit, fmt.Errorf("newroot: stat %s: %w", newroot, err)
	}
	if !newSt.IsDir() {
		return newinit, fmt.Errorf("newroot: %s is not a directory", newroot)
	}
	// Verify newroot is on a different filesystem than / — the whole
	// point is switching filesystems, and forgetting to mount the
	// real root beforehand is the #1 initramfs bug.
	oldSt, err := os.Stat("/")
	if err != nil {
		return newinit, fmt.Errorf("newroot: stat /: %w", err)
	}
	newSys, newOk := newSt.Sys().(*syscall.Stat_t)
	oldSys, oldOk := oldSt.Sys().(*syscall.Stat_t)
	if !newOk || !oldOk {
		return newinit, errors.New("newroot: unable to compare filesystem devices")
	}
	if newSys.Dev == oldSys.Dev {
		return newinit, fmt.Errorf("newroot: %s is not a mount point (same device as /)", newroot)
	}
	// Verify init exists under newroot, is a regular file, and is
	// executable. Missing init here = the operator forgot to install
	// the real userspace before triggering the switch — a common
	// initramfs mistake.
	initPath := filepath.Join(newroot, newinit)
	initSt, err := os.Stat(initPath)
	if err != nil {
		return newinit, fmt.Errorf("newinit: stat %s: %w", initPath, err)
	}
	if !initSt.Mode().IsRegular() {
		return newinit, fmt.Errorf("newinit: %s is not a regular file", initPath)
	}
	if initSt.Mode()&0111 == 0 {
		return newinit, fmt.Errorf("newinit: %s is not executable", initPath)
	}
	return newinit, nil
}

// Do performs the switch-root transition. Does NOT return on
// success — the new init is exec'd in place. On failure past the
// point-of-no-return (mount moves), returns an error whose only
// sensible response is to drop to a rescue shell or panic; the
// caller (control handler + PID-1 main) should decide policy.
//
// Precondition: Precheck must have returned nil for the same
// arguments. Do re-checks the fast invariants (PID 1, newroot
// stat) but not every disk-touching step — the caller committed
// after the precheck.
func Do(newroot, newinit string, logger *logging.Logger, stopServices func()) error {
	// Re-check PID 1 defensively.
	if os.Getpid() != 1 {
		return ErrNotPID1
	}
	if newinit == "" {
		newinit = defaultInit
	}

	logger.Warn("switch-root: transitioning to %s (init=%s)", newroot, newinit)

	// 1. Bring services down cleanly. The caller provides the
	//    shutdown hook so this package doesn't need to know about
	//    pkg/service internals; slinit's main wires it to the same
	//    orderly stop the reboot path uses.
	if stopServices != nil {
		logger.Info("switch-root: stopping services")
		stopServices()
	}

	// 2. SIGTERM every process except PID 1 (kernel does not deliver
	//    signal 1 sends to us). Wait killSettle. Then SIGKILL any
	//    holdouts. Same pattern as pkg/shutdown.KillAllProcesses,
	//    but we don't reuse that entrypoint because it's tied to the
	//    shutdown code path which finishes with reboot(2).
	logger.Info("switch-root: signalling all processes (SIGTERM)")
	killAllFunc(unix.SIGTERM)
	sleepFunc(killSettle)
	logger.Info("switch-root: signalling all processes (SIGKILL)")
	killAllFunc(unix.SIGKILL)
	reapFunc()

	// Capture old root device before we start moving mounts — the
	// initramfs cleanup step below needs it to identify "files that
	// still belong to old root" vs "files that moved into newroot".
	var oldRootStat unix.Stat_t
	if err := unix.Stat("/", &oldRootStat); err != nil {
		return fmt.Errorf("switch-root: stat /: %w", err)
	}
	oldRootDev := oldRootStat.Dev

	// 3. Move virtual filesystems into newroot. Try all four even
	//    if one fails — a bad /dev shouldn't also skip /proc, /sys,
	//    /run. mount --move is atomic; the mount vanishes from the
	//    old namespace and reappears at the new path in the same
	//    kernel op.
	logger.Info("switch-root: moving virtual filesystems")
	for _, m := range []string{"/dev", "/proc", "/sys", "/run"} {
		if err := moveMount(m, newroot, oldRootDev); err != nil {
			// Log-and-continue: a partial move is recoverable in
			// principle (new-root's own devtmpfs etc. may take over)
			// and we've already killed userspace, so we can't reply.
			logger.Error("switch-root: %v", err)
		}
	}

	// 4. If the old root is initramfs (ramfs/tmpfs), free that RAM by
	//    deleting the old-root contents. Best-effort — errors here
	//    cost some RAM but don't fail the switch. Skipped silently
	//    when / is a real block-backed fs (safety net against a
	//    caller mistakenly pointing switch-root at a bind-mount).
	//    Must happen BEFORE the mount-move-to-/ below, otherwise /
	//    would point at newroot and we'd start deleting the wrong
	//    tree.
	if removeAllFunc != nil {
		removeAllFunc(logger, newroot)
	}

	// 5. Perform the actual pivot: chdir newroot, mount --move to /,
	//    chroot, chdir. Broken into a hookable helper so tests can
	//    validate the sequence without changing the test binary's
	//    root.
	if err := pivotChrootFunc(newroot); err != nil {
		return fmt.Errorf("switch-root: pivot failed: %w", err)
	}

	// 5. Reopen /dev/console → stdin/stdout/stderr in the new root.
	//    The old console fd was pointing at the initramfs /dev/console
	//    which is now moved-in-place at newroot/dev/console (post-
	//    mount-move) and the chroot rebinds "/" so /dev/console
	//    resolves under the new root. Best-effort.
	if fd, err := syscall.Open("/dev/console", syscall.O_RDWR, 0); err == nil {
		_ = syscall.Dup2(fd, 0)
		_ = syscall.Dup2(fd, 1)
		_ = syscall.Dup2(fd, 2)
		if fd > 2 {
			_ = syscall.Close(fd)
		}
	}

	// 6. Exec the new init. env carries whatever the initramfs slinit
	//    exported — the new init picks up its own env from /etc/…
	//    files as it starts. argv[0] is the init path itself, mirroring
	//    how the kernel would have invoked it on a normal boot.
	logger.Warn("switch-root: executing %s", newinit)
	if err := execFunc(newinit, []string{newinit}, os.Environ()); err != nil {
		return fmt.Errorf("switch-root: exec %s: %w", newinit, err)
	}
	// Unreachable on success.
	return errors.New("switch-root: exec returned unexpectedly")
}

// moveMount performs `mount --move oldpath newroot/oldpath` when
// oldpath is actually a mount point (device differs from /). No-ops
// on plain directories so re-invoking after a partial move is safe.
func moveMount(oldpath, newroot string, oldRootDev uint64) error {
	var st unix.Stat_t
	if err := unix.Stat(oldpath, &st); err != nil {
		return nil // not mounted, skip silently
	}
	if st.Dev == oldRootDev {
		return nil // plain directory on old root, no move needed
	}
	newpath := filepath.Join(newroot, oldpath)
	// Ensure the target directory exists — under a fresh newroot the
	// tree may not have /proc etc. pre-created.
	if err := os.MkdirAll(newpath, 0o755); err != nil {
		return fmt.Errorf("move %s → %s: mkdir target: %w", oldpath, newpath, err)
	}
	if err := mountFunc(oldpath, newpath, "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move %s → %s: %w", oldpath, newpath, err)
	}
	return nil
}

// defaultPivotChroot implements the chdir + mount-move + chroot +
// chdir sequence that makes newroot the new / . Deliberately does
// NOT use PivotRoot(2): pivot_root requires both paths to be mount
// points AND requires an existing put_old, which the initramfs
// workflow doesn't guarantee — the initramfs is a ramfs (not a
// mount point in the pivot_root sense). Match finit's approach:
// mount --move newroot /, then chroot. This works for initramfs +
// initrd + a plain real-fs newroot alike.
func defaultPivotChroot(newroot string) error {
	if err := os.Chdir(newroot); err != nil {
		return fmt.Errorf("chdir %s: %w", newroot, err)
	}
	if err := mountFunc(newroot, "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("mount --move %s /: %w", newroot, err)
	}
	if err := syscall.Chroot("."); err != nil {
		return fmt.Errorf("chroot .: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	return nil
}

// defaultKillAll sends signo to every process except PID 1.
// kill(-1, signo) semantics per POSIX: -1 = all processes the caller
// may signal, which as PID 1 with CAP_KILL is every non-kernel-thread
// pid.
func defaultKillAll(signo unix.Signal) {
	_ = syscall.Kill(-1, signo)
}

// defaultReap drains any zombie children left behind by the SIGKILL
// sweep so their pid slots free before the exec.
func defaultReap() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err != nil || pid <= 0 {
			return
		}
	}
}

// defaultRemoveOldRoot is the initramfs cleanup pass. Only fires
// when statfs("/") shows RAMFS_MAGIC or TMPFS_MAGIC — we mustn't
// rm the contents of a real block-device root when a caller
// mistakenly points switch-root at a directory bind-mounted from
// the same root. Best-effort; errors log-and-continue.
func defaultRemoveOldRoot(logger *logging.Logger, newroot string) {
	var sfs unix.Statfs_t
	if err := statfsFunc("/", &sfs); err != nil {
		return
	}
	const (
		ramfsMagic = 0x858458F6
		tmpfsMagic = 0x01021994
	)
	if sfs.Type != ramfsMagic && sfs.Type != tmpfsMagic {
		return
	}
	// Walk / and remove entries that are NOT part of newroot's
	// subtree and that live on the same device as /. Extremely
	// conservative to avoid deleting something bind-mounted from
	// elsewhere.
	entries, err := os.ReadDir("/")
	if err != nil {
		logger.Warn("switch-root: cleanup: readdir /: %v", err)
		return
	}
	skipRel, _ := filepath.Rel("/", newroot)
	for _, e := range entries {
		if e.Name() == skipRel {
			continue
		}
		p := filepath.Join("/", e.Name())
		if err := os.RemoveAll(p); err != nil {
			logger.Info("switch-root: cleanup: %s: %v", p, err)
		}
	}
}
