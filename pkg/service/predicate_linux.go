package service

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// mountStatx is unix.Statx; tests replace it to stand in for kernels
// that lack STATX_ATTR_MOUNT_ROOT or statx itself.
var mountStatx = unix.Statx

// pathIsMountPoint returns true iff path is a mount point, the way
// systemd's path_is_mount_point(AT_SYMLINK_FOLLOW) decides it: the
// kernel's STATX_ATTR_MOUNT_ROOT (Linux 5.8), which says whether the
// path is the root of a mount. Comparing the device with the parent's,
// as slinit did, cannot see a bind mount within one filesystem (same
// device on both sides) and takes a btrfs subvolume (a device of its
// own, no mount) for one. Kernels without the attribute fall back to
// comparing mount ids (STATX_MNT_ID) and, before statx, the device.
//
// The path is resolved first, as systemd's AT_SYMLINK_FOLLOW does: the
// parent compared against is the parent of the directory the path
// names, so "/proc/" and "/proc/." are /proc, and a symlink is judged
// by its target rather than by the directory the link sits in.
func pathIsMountPoint(path string) (bool, string) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if filepath.Clean(path) == "/" {
		return true, ""
	}
	notMount := func() (bool, string) { return false, fmt.Sprintf("%q is not a mount point", path) }
	// AT_NO_AUTOMOUNT|AT_STATX_DONT_SYNC as systemd: whether a path is
	// a mount is local, and must not trigger an automount or a network
	// round trip.
	const flags = unix.AT_NO_AUTOMOUNT | unix.AT_STATX_DONT_SYNC
	var stx unix.Statx_t
	err := mountStatx(unix.AT_FDCWD, path, flags, unix.STATX_TYPE|unix.STATX_INO|unix.STATX_MNT_ID, &stx)
	if errors.Is(err, unix.ENOSYS) {
		return pathIsMountPointByDev(path)
	}
	if err != nil {
		return false, fmt.Sprintf("path %q: %v", path, err)
	}
	if stx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT != 0 {
		if stx.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0 {
			return true, ""
		}
		// In a chroot the root directory need not be a mount root,
		// yet it is "/" to everything under it.
		var root unix.Statx_t
		if mountStatx(unix.AT_FDCWD, "/", unix.AT_STATX_DONT_SYNC, unix.STATX_TYPE|unix.STATX_INO, &root) == nil &&
			root.Dev_major == stx.Dev_major && root.Dev_minor == stx.Dev_minor && root.Ino == stx.Ino {
			return true, ""
		}
		return notMount()
	}
	if stx.Mask&unix.STATX_MNT_ID != 0 {
		parent := filepath.Dir(path)
		var pstx unix.Statx_t
		if err := mountStatx(unix.AT_FDCWD, parent, flags, unix.STATX_MNT_ID, &pstx); err != nil {
			return false, fmt.Sprintf("parent %q: %v", parent, err)
		}
		if pstx.Mask&unix.STATX_MNT_ID != 0 {
			if stx.Mnt_id != pstx.Mnt_id {
				return true, ""
			}
			return notMount()
		}
	}
	return pathIsMountPointByDev(path)
}

// pathIsMountPointByDev is the last resort, for kernels whose statx
// reports neither the mount-root attribute nor the mount id: the device
// differs from the parent's.
func pathIsMountPointByDev(path string) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("path %q: %v", path, err)
	}
	sysStat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Sprintf("path %q: cannot read stat", path)
	}
	parent := filepath.Dir(path)
	pst, err := os.Stat(parent)
	if err != nil {
		return false, fmt.Sprintf("parent %q: %v", parent, err)
	}
	psysStat, ok := pst.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Sprintf("parent %q: cannot read stat", parent)
	}
	if sysStat.Dev != psysStat.Dev {
		return true, ""
	}
	return false, fmt.Sprintf("%q is not a mount point", path)
}

// predicateRoot prefixes the fixed host paths that the machine
// predicates read (/proc, /sys, /etc, /run), so a test can point them
// at a fixture tree. Empty outside tests: each path is read exactly as
// written.
var predicateRoot = ""

// hostPath is p under predicateRoot.
func hostPath(p string) string { return predicateRoot + p }

// bootCondActivePath is the kernel-cmdline snapshot to look for
// slinit.cond=... in. Overridable at package level for tests that
// mock a specific cmdline shape.
var bootCondActivePath = "/proc/cmdline"

// checkBootCond returns true when the operator booted with
// slinit.cond=... on the kernel command line and the comma-separated
// value list contains `name`. finit-parity for the
// `finit.cond=foo,bar` factory/upgrade mode selector: services with
// `condition-boot-cond = factory` only start when the operator
// picked factory mode at boot. Multiple slinit.cond= tokens on the
// same cmdline are all concatenated (matches finit's semantics).
// Empty `name` never matches (guards against a config typo that
// silently pins the service to "starts always").
func checkBootCond(name string) (bool, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, "condition-boot-cond: empty tag"
	}
	data, err := os.ReadFile(bootCondActivePath)
	if err != nil {
		return false, fmt.Sprintf("%s: %v", bootCondActivePath, err)
	}
	tokens := strings.Fields(string(data))
	for _, t := range tokens {
		// Accept both slinit.cond=... (native) and finit.cond=...
		// (parity with a finit boot cmdline the operator may not
		// have updated yet).
		var value string
		switch {
		case strings.HasPrefix(t, "slinit.cond="):
			value = t[len("slinit.cond="):]
		case strings.HasPrefix(t, "finit.cond="):
			value = t[len("finit.cond="):]
		default:
			continue
		}
		for _, v := range strings.Split(value, ",") {
			if strings.TrimSpace(v) == name {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf("slinit.cond= list lacks %q", name)
}

// kernelCmdlineContains looks for a whole-word token in /proc/cmdline.
// systemd's ConditionKernelCommandLine matches either "key" presence or
// "key=value" exactly; we do the same. An empty file means "no kernel
// cmdline available" which is reported as missing.
func kernelCmdlineContains(token string) (bool, string) {
	data, err := os.ReadFile(hostPath("/proc/cmdline"))
	if err != nil {
		return false, fmt.Sprintf("/proc/cmdline: %v", err)
	}
	tokens := strings.Fields(string(data))
	want := strings.TrimSpace(token)
	if strings.ContainsRune(want, '=') {
		for _, t := range tokens {
			if t == want {
				return true, ""
			}
		}
	} else {
		for _, t := range tokens {
			if t == want {
				return true, ""
			}
			if i := strings.IndexByte(t, '='); i >= 0 && t[:i] == want {
				return true, ""
			}
		}
	}
	return false, fmt.Sprintf("kernel cmdline lacks %q", token)
}

// checkFirstBoot checks whether this is the OS's first boot. systemd
// uses /etc/machine-id == "uninitialized" or a missing /etc/machine-id
// as the marker. We accept either form. param "yes" (default) succeeds
// on first boot; "no" succeeds on subsequent boots.
func checkFirstBoot(param string) (bool, string) {
	first := isFirstBoot()
	switch strings.TrimSpace(param) {
	case "", "yes":
		if first {
			return true, ""
		}
		return false, "not a first boot"
	case "no":
		if !first {
			return true, ""
		}
		return false, "is first boot"
	}
	return false, fmt.Sprintf("invalid first-boot param %q", param)
}

func isFirstBoot() bool {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		// A missing /etc/machine-id is the strongest first-boot signal.
		return os.IsNotExist(err)
	}
	s := strings.TrimSpace(string(data))
	return s == "" || s == "uninitialized"
}

// checkHostMatch compares param against the system hostname (case
// insensitive). A leading "!" is honoured by the outer Negate field, so
// callers shouldn't pass it here.
func checkHostMatch(param string) (bool, string) {
	host, err := os.Hostname()
	if err != nil {
		return false, fmt.Sprintf("hostname: %v", err)
	}
	if strings.EqualFold(host, strings.TrimSpace(param)) {
		return true, ""
	}
	return false, fmt.Sprintf("host %q != %q", host, param)
}

// checkSecurity returns true when the named LSM is active. systemd's
// ConditionSecurity recognises selinux/apparmor/tomoyo/ima/audit/smack.
// We probe via /sys/kernel/security/<lsm>/ and /proc/self/attr.
func checkSecurity(param string) (bool, string) {
	want := strings.ToLower(strings.TrimSpace(param))
	switch want {
	case "selinux":
		if _, err := os.Stat(hostPath("/sys/fs/selinux")); err == nil {
			return true, ""
		}
	case "apparmor":
		if _, err := os.Stat(hostPath("/sys/kernel/security/apparmor")); err == nil {
			return true, ""
		}
	case "tomoyo":
		if _, err := os.Stat(hostPath("/sys/kernel/security/tomoyo")); err == nil {
			return true, ""
		}
	case "smack":
		if _, err := os.Stat(hostPath("/sys/fs/smackfs")); err == nil {
			return true, ""
		}
	case "ima":
		if _, err := os.Stat(hostPath("/sys/kernel/security/ima")); err == nil {
			return true, ""
		}
	case "audit":
		// systemd's use_audit: the kernel's audit netlink socket can be
		// opened and answers. Audit is not an LSM, so the LSM list read
		// before never named it and the check was always false.
		if auditUsable(auditProbe()) {
			return true, ""
		}
	case "measured-os":
		return measuredOS()
	case "measured-uki":
		// Stricter sibling of measured-os: requires evidence that a
		// UKI (systemd-stub or equivalent) measured the kernel image
		// into a TPM PCR at boot. sd-stub advertises this by writing
		// the LoaderVendor EFI variable StubPcrKernelImage=<pcr>.
		return measuredUKI()
	default:
		return false, fmt.Sprintf("unknown security framework %q", param)
	}
	return false, fmt.Sprintf("security framework %q not active", param)
}

// checkNeedsUpdate has two forms. slinit's own, "yes" (the default)
// or "no", looks for the marker files /run/systemd/update-on-next-boot
// and /run/needs-update. systemd's, an absolute directory (/etc, /var),
// asks whether /usr changed after that directory's .updated stamp
// (needsUpdateDir); slinit rejected it until 4.1, so taking it now
// changes no working config.
func checkNeedsUpdate(param string) (bool, string) {
	p := strings.TrimSpace(param)
	if strings.HasPrefix(p, "/") {
		return needsUpdateDir(p)
	}
	pending := false
	if _, err := os.Stat(hostPath("/run/systemd/update-on-next-boot")); err == nil {
		pending = true
	} else if _, err := os.Stat(hostPath("/run/needs-update")); err == nil {
		pending = true
	}
	switch p {
	case "", "yes":
		if pending {
			return true, ""
		}
		return false, "no update marker present"
	case "no":
		if !pending {
			return true, ""
		}
		return false, "update marker present"
	}
	return false, fmt.Sprintf("invalid needs-update param %q", param)
}

// checkACPower reads /sys/class/power_supply to determine whether the
// system is on AC. With no power supply class entries we treat the
// system as AC-powered (desktop / VM / bare-metal server).
func checkACPower(param string) (bool, string) {
	onAC, hasInfo := detectACPower()
	if !hasInfo {
		// Best-effort default: assume AC.
		onAC = true
	}
	switch strings.TrimSpace(param) {
	case "", "yes":
		if onAC {
			return true, ""
		}
		return false, "system is on battery"
	case "no":
		if !onAC {
			return true, ""
		}
		return false, "system is on AC"
	}
	return false, fmt.Sprintf("invalid ac-power param %q", param)
}

func detectACPower() (bool, bool) {
	entries, err := os.ReadDir(hostPath("/sys/class/power_supply"))
	if err != nil || len(entries) == 0 {
		return false, false
	}
	// If any AC adapter reports online=1, we're on AC.
	for _, e := range entries {
		typPath := filepath.Join(hostPath("/sys/class/power_supply"), e.Name(), "type")
		t, err := os.ReadFile(typPath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(t)) == "Mains" {
			online, err := os.ReadFile(filepath.Join(hostPath("/sys/class/power_supply"), e.Name(), "online"))
			if err != nil {
				continue
			}
			if strings.TrimSpace(string(online)) == "1" {
				return true, true
			}
		}
	}
	// No Mains-on reported → on battery iff at least one battery exists.
	for _, e := range entries {
		typPath := filepath.Join(hostPath("/sys/class/power_supply"), e.Name(), "type")
		t, err := os.ReadFile(typPath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(t)) == "Battery" {
			return false, true
		}
	}
	return false, false
}

// checkNetworkOnline reports whether the machine has usable networking,
// using launchd's definition of "up" verbatim: at least one
// non-loopback interface that is up and has an IPv4 or IPv6 address
// assigned. Adapted from KeepAlive{NetworkState}.
//
// All three parts of that definition matter, and dropping any one of
// them is how this check starts lying. An interface that is up with no
// address cannot carry traffic. A loopback that is up always is, so
// counting it makes the predicate constant. And an address on a down
// interface is configuration, not connectivity.
//
// This says nothing about reachability — no route is consulted and
// nothing is pinged. An interface with a link-local address and no
// gateway reads as online, because that is what launchd reports too and
// because a predicate that probes the network would block a start on a
// timeout.
func checkNetworkOnline(param string) (bool, string) {
	online, detail := detectNetworkOnline()
	switch strings.TrimSpace(param) {
	case "", "yes":
		if online {
			return true, ""
		}
		return false, "no non-loopback interface is up with an address"
	case "no":
		if !online {
			return true, ""
		}
		return false, "network is up (" + detail + ")"
	}
	return false, fmt.Sprintf("invalid network-online param %q", param)
}

// detectNetworkOnline returns whether a qualifying interface exists and,
// when one does, names it for the failure message of the negated form.
func detectNetworkOnline() (bool, string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		// Unknowable rather than false: refusing every start because
		// the interface list could not be read would turn a transient
		// kernel or permission problem into a boot failure.
		return true, "interface list unavailable"
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsUnspecified() {
				return true, ifi.Name + " " + ip.String()
			}
		}
	}
	return false, ""
}
