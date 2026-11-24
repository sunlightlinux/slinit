package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// sandboxSpec is the parsed set of slinit-runner flags that drive the
// filesystem-sandbox setup. Fields mirror their systemd counterparts;
// see slinit-service(5) for user-facing semantics.
type sandboxSpec struct {
	privateTmp          bool
	protectSystem       string // "" | "yes" | "full" | "strict"
	readOnlyPaths       []string
	readWritePaths      []string
	protectHome         string // "" | "yes" | "read-only" | "tmpfs"
	inaccessiblePaths   []string
	protectProc         string   // "" | "noaccess" | "invisible" | "ptraceable"
	procSubset          string   // "" | "pid"
	bindPaths           []string // "src:dst" entries, writable
	bindROPaths         []string // "src:dst" entries, read-only
	temporaryFilesystem []string // "path[:opts]" entries
}

// active reports whether any sandbox knob is set. Used by main() to
// skip the (privileged, ns-clobbering) setup path entirely when nothing
// was requested.
func (s sandboxSpec) active() bool {
	return s.privateTmp ||
		s.protectSystem != "" || len(s.readOnlyPaths) > 0 || len(s.readWritePaths) > 0 ||
		s.protectHome != "" || len(s.inaccessiblePaths) > 0 ||
		s.protectProc != "" || s.procSubset != "" ||
		len(s.bindPaths) > 0 || len(s.bindROPaths) > 0 ||
		len(s.temporaryFilesystem) > 0
}

// applySandbox configures the calling task's mount namespace per spec.
// It assumes the runner has already been clone()'d into a fresh
// CLONE_NEWNS by the parent — the kernel propagates the parent's mounts
// into it, so the first thing this function does is mark them MS_PRIVATE
// so subsequent mount(2) calls don't leak back to the host.
//
// Order of operations (matters):
//  1. Detach the namespace: rec MS_PRIVATE on /
//  2. PrivateTmp — replace /tmp and /var/tmp with fresh tmpfs (so
//     ProtectSystem doesn't have to special-case them)
//  3. ProtectSystem — ro remount of system paths
//  4. ReadWritePaths — punch writable holes through ProtectSystem
//  5. ReadOnlyPaths — additional ro overlays on writable paths
//
// On any failure the sandbox fails closed: a half-applied sandbox is
// indistinguishable from the host filesystem to the service, which is
// exactly the surprise we must not produce.
func applySandbox(s sandboxSpec) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make / private: %w", err)
	}

	// Order matches systemd's documented application order: replace
	// virtual filesystems and broad protections first, then layer
	// specific overrides on top. The earlier MS_PRIVATE makes every
	// subsequent mount(2) confined to this namespace.

	if s.privateTmp {
		if err := mountPrivateTmpfs("/tmp"); err != nil {
			return err
		}
		if err := mountPrivateTmpfs("/var/tmp"); err != nil {
			return err
		}
	}

	if err := applyProtectHome(s.protectHome); err != nil {
		return err
	}

	for _, entry := range s.temporaryFilesystem {
		if err := mountTmpfsEntry(entry); err != nil {
			return err
		}
	}

	// The writable mounts made above stay writable under protect-system,
	// as systemd keeps them.
	var keep []string
	for _, entry := range s.temporaryFilesystem {
		path, _, _ := strings.Cut(entry, ":")
		keep = append(keep, path)
	}
	if s.protectHome == "tmpfs" {
		keep = append(keep, "/home", "/root", "/run/user")
	}
	if err := applyProtectSystem(s.protectSystem, keep); err != nil {
		return err
	}

	if err := applyProtectProc(s.protectProc, s.procSubset); err != nil {
		return err
	}

	// Bind-mounts: writable first so a read-only overlay later (either
	// from BindReadOnlyPaths or ReadOnlyPaths) can still re-flip the
	// same dst to ro. systemd's order is the same.
	for _, entry := range s.bindPaths {
		if err := bindPathEntry(entry, false); err != nil {
			return err
		}
	}
	for _, entry := range s.bindROPaths {
		if err := bindPathEntry(entry, true); err != nil {
			return err
		}
	}

	for _, p := range s.readWritePaths {
		if err := bindMount(p, p, false); err != nil {
			return fmt.Errorf("read-write-path %q: %w", p, err)
		}
	}

	for _, p := range s.readOnlyPaths {
		if err := bindMount(p, p, true); err != nil {
			return fmt.Errorf("read-only-path %q: %w", p, err)
		}
	}

	// Inaccessible paths come last so an earlier bind cannot
	// accidentally unhide them.
	for _, p := range s.inaccessiblePaths {
		if err := mountInaccessible(p); err != nil {
			return fmt.Errorf("inaccessible-path %q: %w", p, err)
		}
	}

	return nil
}

// applyProtectHome hides /home, /root and /run/user according to mode:
//   - "yes"       → over-mount with an empty inaccessible tmpfs
//   - "read-only" → ro remount via bind
//   - "tmpfs"     → fresh empty per-service tmpfs
//
// Paths that don't exist are silently skipped (minimal containers may
// legitimately lack /root or /run/user).
func applyProtectHome(mode string) error {
	if mode == "" {
		return nil
	}
	targets := []string{"/home", "/root", "/run/user"}
	for _, t := range targets {
		if _, err := os.Stat(t); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("protect-home stat %s: %w", t, err)
		}
		switch mode {
		case "yes":
			if err := mountInaccessible(t); err != nil {
				return fmt.Errorf("protect-home %s: %w", t, err)
			}
		case "read-only":
			if err := remountRO(t); err != nil {
				return fmt.Errorf("protect-home %s: %w", t, err)
			}
		case "tmpfs":
			if err := unix.Mount("tmpfs", t, "tmpfs",
				unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
				return fmt.Errorf("protect-home tmpfs %s: %w", t, err)
			}
		default:
			return fmt.Errorf("unknown protect-home mode %q", mode)
		}
	}
	return nil
}

// applyProtectProc mounts a new procfs instance on /proc with hidepid=
// and/or subset= options, as systemd's mount_procfs does.
//
// It used to remount the /proc it found. procfs options belong to the
// superblock, not to the mount, and CLONE_NEWNS copies mounts but shares
// their superblocks — so the remount, made in the service's own mount
// namespace, changed the host's /proc too. After one service with
// protect-proc + proc-subset the whole system saw hidepid=invisible,
// subset=pid: /proc/meminfo, /proc/stat and /proc/uptime were gone, and
// htop, free and uptime stopped working. A new instance is the service's
// alone. (A remount that changes subset= is also refused with EINVAL by
// newer kernels, so the old way failed there outright.)
func applyProtectProc(hidepid, subset string) error {
	if hidepid == "" && subset == "" {
		return nil
	}
	var opts []string
	if hidepid != "" {
		opts = append(opts, "hidepid="+hidepid)
	}
	if subset != "" {
		opts = append(opts, "subset="+subset)
	}
	data := strings.Join(opts, ",")
	if err := unix.Mount("proc", "/proc", "proc",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, data); err != nil {
		return fmt.Errorf("mount /proc with %q: %w", data, err)
	}
	return nil
}

// mountTmpfsEntry mounts a fresh tmpfs at the requested path. Entries
// have the form "path" or "path:options" where options is forwarded
// verbatim to mount(2) (e.g. "size=64m,mode=0700"). Missing target dirs
// are created so the operator does not have to script that on top.
func mountTmpfsEntry(entry string) error {
	path, opts, _ := strings.Cut(entry, ":")
	if path == "" {
		return fmt.Errorf("tmpfs entry has no path: %q", entry)
	}
	if err := os.MkdirAll(path, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("tmpfs mkdir %s: %w", path, err)
	}
	if err := unix.Mount("tmpfs", path, "tmpfs",
		unix.MS_NOSUID|unix.MS_NODEV, opts); err != nil {
		return fmt.Errorf("tmpfs mount %s: %w", path, err)
	}
	return nil
}

// bindPathEntry processes one "src:dst" entry from --bind-path or
// --bind-ro-path. The parser has already canonicalised both sides; here
// we only ensure dst exists (creating it as a directory if missing,
// matching systemd's documented behaviour) and then do the bind.
func bindPathEntry(entry string, ro bool) error {
	src, dst, ok := strings.Cut(entry, ":")
	if !ok {
		return fmt.Errorf("bind entry missing ':' delimiter: %q", entry)
	}
	if _, err := os.Stat(dst); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("bind stat %s: %w", dst, err)
		}
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return fmt.Errorf("bind mkdir %s: %w", dst, err)
		}
	}
	return bindMount(src, dst, ro)
}

// mountInaccessible over-mounts target with an empty restrictively-
// permissioned mount, hiding whatever was there from the service. The
// systemd implementation uses a per-target inaccessible inode in
// /run/systemd/inaccessible/; we use a fresh tmpfs (which is logically
// identical from the service's point of view) since slinit has no such
// helper directory and shipping one is unnecessary complexity.
func mountInaccessible(target string) error {
	st, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inaccessible stat %s: %w", target, err)
	}
	if st.IsDir() {
		if err := unix.Mount("tmpfs", target, "tmpfs",
			unix.MS_NOSUID|unix.MS_NODEV|unix.MS_RDONLY, "mode=0000"); err != nil {
			return fmt.Errorf("inaccessible tmpfs %s: %w", target, err)
		}
		return nil
	}
	// Non-directory: shadow it with /dev/null. The service then sees an
	// empty unreadable file, which matches the systemd semantics.
	if err := unix.Mount("/dev/null", target, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("inaccessible bind /dev/null over %s: %w", target, err)
	}
	return nil
}

// mountPrivateTmpfs replaces target with a fresh tmpfs (mode 1777). The
// service sees an empty /tmp that nothing else can read or write —
// systemd's PrivateTmp= semantics. We pass nosuid/nodev to match
// systemd's defaults; the size is left at the kernel's default
// (half of RAM) for now.
func mountPrivateTmpfs(target string) error {
	if _, err := os.Stat(target); err != nil {
		// target missing — nothing to shadow, treat as success.
		// /var/tmp may be absent in minimal containers.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("private-tmp stat %s: %w", target, err)
	}
	if err := unix.Mount("tmpfs", target, "tmpfs",
		unix.MS_NOSUID|unix.MS_NODEV, "mode=1777"); err != nil {
		return fmt.Errorf("private-tmp mount %s: %w", target, err)
	}
	return nil
}

// applyProtectSystem ro-remounts the system paths matching the requested
// level, with every mount below them. "yes" → /usr,/boot,/efi; "full"
// adds /etc; "strict" makes the whole rootfs ro except for the standard
// writable mountpoints and any caller-supplied read-write-paths (which
// are bind-mounted writable after this returns). Mounts at or below keep
// are left as they are.
//
// remountRO is silent for paths that don't exist — minimal containers
// often lack /boot or /efi and that should not fail the start.
func applyProtectSystem(level string, keep []string) error {
	switch level {
	case "":
		return nil
	case "yes":
		return remountROAll([]string{"/usr", "/boot", "/efi"}, keep)
	case "full":
		return remountROAll([]string{"/usr", "/boot", "/efi", "/etc"}, keep)
	case "strict":
		// "strict" means ro everywhere except the carve-outs systemd
		// keeps writable by default (devices, kernel interfaces,
		// volatile state) plus any caller-supplied read-write-paths.
		// The simplest correct implementation is: remount / ro, then
		// the rwPaths loop in applySandbox punches writable holes
		// back through. /dev, /proc, /sys and /run stay writable, being
		// separate mounts. /tmp and /var/tmp do only where they are too: on
		// a system without a tmpfs there they go read-only with /.
		keep = append([]string{"/dev", "/proc", "/sys", "/run", "/tmp", "/var/tmp"}, keep...)
		return remountROExcept("/", keep)
	default:
		return fmt.Errorf("unknown protect-system level %q", level)
	}
}

func remountROAll(paths, keep []string) error {
	for _, p := range paths {
		if err := remountROExcept(p, keep); err != nil {
			return err
		}
	}
	return nil
}

// remountRO marks an existing mount point as read-only, and every mount
// below it. For a path that isn't already a mount point we first bind it
// onto itself so the kernel will accept the MS_REMOUNT (the
// remount-with-MS_RDONLY flag only applies to mount points). Paths that
// don't exist are silently skipped: minimal rootfs may legitimately lack
// /boot or /efi.
func remountRO(path string) error {
	return remountROExcept(path, nil)
}

// remountROExcept is remountRO leaving the mounts at or below keep as
// they are.
func remountROExcept(path string, keep []string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("protect-system stat %s: %w", path, err)
	}
	// Bind first so we have something to remount, then flip ro. The
	// bind silently succeeds for an already-mounted path; flipping ro
	// is the operation we actually care about.
	if err := unix.Mount(path, path, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("protect-system bind %s: %w", path, err)
	}
	if err := remountTreeRO(path, keep); err != nil {
		return fmt.Errorf("protect-system %w", err)
	}
	return nil
}

// bindMount bind-mounts src onto dst. When ro is true we follow the
// kernel's two-step quirk: bind first, then remount with MS_RDONLY
// (the kernel ignores the rdonly flag on the initial bind). Recursive
// (MS_REC) so a directory tree containing other mount points propagates
// the visibility as the operator expects.
func bindMount(src, dst string, ro bool) error {
	if err := unix.Mount(src, dst, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind %s -> %s: %w", src, dst, err)
	}
	if !ro {
		// A bind inherits the flags of the mount it comes from, so under a
		// read-only / a read-write path stayed read-only until said otherwise.
		return remountTreeRW(src, dst)
	}
	return remountTreeRO(dst, nil)
}

// madeRO holds the mount points remountTreeRO turned read-only, so that
// a writable bind made later turns back exactly those and leaves read-only
// what was read-only before the sandbox.
var madeRO = map[string]bool{}

// remountTreeRO turns top read-only and with it every mount below top
// that is not at or below keep. A remount acts on the one mount at the
// path it is given, so the submounts a recursive bind brings along stayed
// writable until each got its own: /proc/sys/fs/binfmt_misc under
// protect-kernel-tunables, /run/user/UID under protect-home=read-only.
//
// Each remount repeats the mount's own flags: a bind remount replaces
// them, so MS_RDONLY alone cleared nosuid, nodev and noexec, and where
// they are locked (a user namespace below the mount's) failed with EPERM.
func remountTreeRO(top string, keep []string) error {
	tree, err := mountTree(top)
	if err != nil {
		return err
	}
	for i, m := range tree {
		if i > 0 && underAny(m.point, keep) {
			continue
		}
		err := unix.Mount("", m.point, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|m.flags, "")
		if err != nil && (i == 0 || !hiddenMount(err)) {
			return fmt.Errorf("remount-ro %s: %w", m.point, err)
		}
		if err == nil && !m.ro {
			madeRO[m.point] = true
		}
	}
	return nil
}

// remountTreeRW makes dst, a recursive bind of src, writable: dst itself,
// and every mount below it whose source remountTreeRO turned read-only.
// The flags it had otherwise are kept, as remountTreeRO keeps them.
func remountTreeRW(src, dst string) error {
	tree, err := mountTree(dst)
	if err != nil {
		return err
	}
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	for i, m := range tree {
		if i > 0 && !madeRO[filepath.Join(realSrc, strings.TrimPrefix(m.point, tree[0].point))] {
			continue
		}
		err := unix.Mount("", m.point, "", unix.MS_REMOUNT|unix.MS_BIND|m.flags, "")
		if err != nil && (i == 0 || !hiddenMount(err)) {
			return fmt.Errorf("remount-rw %s: %w", m.point, err)
		}
	}
	return nil
}

// hiddenMount reports a remount that failed because the mount point is
// no longer reachable at its path: a later mount on a parent covers it
// (ENOENT, or EINVAL for a path that is now no mount point). The service
// cannot reach it either.
func hiddenMount(err error) bool {
	return err == unix.ENOENT || err == unix.EINVAL
}

// mountPoint is one mount listed in /proc/self/mountinfo.
type mountPoint struct {
	point string
	ro    bool
	// flags are the per-mount flags a bind remount must repeat to keep
	// them, MS_RDONLY aside.
	flags uintptr
}

// mountTree lists top, which must be a mount point, and the mounts below
// it that can be remounted, the topmost one for each path. An autofs
// mount is skipped: remounting it would trigger it, and the mount it
// triggers is listed on its own.
func mountTree(top string) ([]mountPoint, error) {
	real, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	tree := parseMountTree(string(data), real)
	if len(tree) == 0 || tree[0].point != real {
		return nil, fmt.Errorf("%s: not a mount point", top)
	}
	return tree, nil
}

// parseMountTree is mountTree on the text of a mountinfo file, top first.
func parseMountTree(mountinfo, top string) []mountPoint {
	type entry struct {
		mountPoint
		autofs bool
	}
	order := []string{top}
	last := map[string]entry{}
	for _, line := range strings.Split(mountinfo, "\n") {
		f := strings.Fields(line)
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(f) {
			continue
		}
		point := unescapeMountinfo(f[4])
		if !underAny(point, []string{top}) {
			continue
		}
		if _, ok := last[point]; !ok && point != top {
			order = append(order, point)
		}
		// A later line for the same path is a mount on top of the
		// earlier one, which is the one a remount of the path reaches.
		e := entry{mountPoint: mountPoint{point: point}, autofs: f[sep+1] == "autofs"}
		e.ro, e.flags = parseMountOpts(f[5])
		last[point] = e
	}
	var out []mountPoint
	for _, p := range order {
		if e, ok := last[p]; ok && !e.autofs {
			out = append(out, e.mountPoint)
		}
	}
	return out
}

// parseMountOpts reads a mountinfo per-mount option list ("rw,nosuid,
// relatime").
func parseMountOpts(opts string) (ro bool, flags uintptr) {
	atime := false
	for _, o := range strings.Split(opts, ",") {
		switch o {
		case "ro":
			ro = true
		case "nosuid":
			flags |= unix.MS_NOSUID
		case "nodev":
			flags |= unix.MS_NODEV
		case "noexec":
			flags |= unix.MS_NOEXEC
		case "nosymfollow":
			flags |= unix.MS_NOSYMFOLLOW
		case "nodiratime":
			flags |= unix.MS_NODIRATIME
		case "noatime":
			flags |= unix.MS_NOATIME
			atime = true
		case "relatime":
			flags |= unix.MS_RELATIME
			atime = true
		}
	}
	// Neither listed is strictatime; left out, the remount would make it
	// relatime.
	if !atime {
		flags |= unix.MS_STRICTATIME
	}
	return ro, flags
}

// underAny reports whether path is one of dirs or lies below one.
func underAny(path string, dirs []string) bool {
	for _, d := range dirs {
		if d == "/" || path == d || strings.HasPrefix(path, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}

// unescapeMountinfo reverses the octal escapes the kernel writes for a
// space, tab, newline or backslash in a mountinfo path.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// stringList implements flag.Value for repeated string args. The runner
// uses it for --read-only-path and --read-write-path which the parent
// emits once per configured path so the operator can mix multiple in a
// single service description.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	if v == "" {
		return fmt.Errorf("empty path")
	}
	*s = append(*s, v)
	return nil
}
