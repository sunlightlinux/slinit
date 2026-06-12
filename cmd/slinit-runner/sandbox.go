package main

import (
	"fmt"
	"os"
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
	protectProc         string // "" | "noaccess" | "invisible" | "ptraceable"
	procSubset          string // "" | "pid"
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

	if err := applyProtectSystem(s.protectSystem, s.readWritePaths); err != nil {
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

// applyProtectProc remounts /proc with hidepid= and/or subset= options
// per the requested mode. /proc must already be a mount point — the
// kernel rejects mount options on non-mount paths, which is exactly
// what we want (no silent no-op).
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
		unix.MS_REMOUNT|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, data); err != nil {
		return fmt.Errorf("remount /proc with %q: %w", data, err)
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
// level. "yes" → /usr,/boot,/efi; "full" adds /etc; "strict" makes the
// whole rootfs ro except for the standard writable mountpoints and any
// caller-supplied read-write-paths (which are bind-mounted writable
// after this returns).
//
// remountRO is silent for paths that don't exist — minimal containers
// often lack /boot or /efi and that should not fail the start.
func applyProtectSystem(level string, rwPaths []string) error {
	switch level {
	case "":
		return nil
	case "yes":
		return remountROAll([]string{"/usr", "/boot", "/efi"})
	case "full":
		return remountROAll([]string{"/usr", "/boot", "/efi", "/etc"})
	case "strict":
		// "strict" means ro everywhere except the carve-outs systemd
		// keeps writable by default (devices, kernel interfaces,
		// volatile state) plus any caller-supplied read-write-paths.
		// The simplest correct implementation is: remount / ro, then
		// the rwPaths loop in applySandbox punches writable holes
		// back through. The kernel mounts kept writable (/dev, /proc,
		// /sys, /run, /tmp, /var/tmp) are separate mount points so a
		// ro remount of / does not touch them.
		return remountRO("/")
	default:
		return fmt.Errorf("unknown protect-system level %q", level)
	}
}

func remountROAll(paths []string) error {
	for _, p := range paths {
		if err := remountRO(p); err != nil {
			return err
		}
	}
	return nil
}

// remountRO marks an existing mount point as read-only. For a path that
// isn't already a mount point we first bind it onto itself so the kernel
// will accept the MS_REMOUNT (the remount-with-MS_RDONLY flag only
// applies to mount points). Paths that don't exist are silently skipped:
// minimal rootfs may legitimately lack /boot or /efi.
func remountRO(path string) error {
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
	if err := unix.Mount("", path, "",
		unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("protect-system remount-ro %s: %w", path, err)
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
		return nil
	}
	if err := unix.Mount("", dst, "",
		unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("remount-ro %s: %w", dst, err)
	}
	return nil
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
