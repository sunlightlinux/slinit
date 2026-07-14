package service

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// pathIsMountPoint returns true iff path is a filesystem mount point.
// Detection: the device id of path differs from the device id of its
// parent. Falls back to checking /proc/self/mountinfo if stat fails.
func pathIsMountPoint(path string) (bool, string) {
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
	// A bind-mounted directory onto its own parent shares the dev but
	// the inode of "/" and "." differs in odd cases — treat anything
	// where path == parent (i.e. /) as a mount point.
	if filepath.Clean(path) == "/" {
		return true, ""
	}
	return false, fmt.Sprintf("%q is not a mount point", path)
}

// kernelCmdlineContains looks for a whole-word token in /proc/cmdline.
// systemd's ConditionKernelCommandLine matches either "key" presence or
// "key=value" exactly; we do the same. An empty file means "no kernel
// cmdline available" which is reported as missing.
func kernelCmdlineContains(token string) (bool, string) {
	data, err := os.ReadFile("/proc/cmdline")
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

// checkVirtualization matches against a probe of the current
// virtualization. The probe order mirrors systemd-detect-virt:
// /proc/1/sched, /proc/cpuinfo "hypervisor", /sys/class/dmi product,
// /proc/sys/kernel/osrelease (Microsoft WSL marker). param accepts:
//
//	"" or "yes" → succeed iff any virtualization is detected
//	"no"        → succeed iff none is detected
//	specific    → succeed iff the probe equals "specific" (e.g. "kvm",
//	              "qemu", "vmware", "lxc", "docker", "wsl")
func checkVirtualization(param string) (bool, string) {
	got := detectVirtualization()
	switch param {
	case "", "yes":
		if got != "" {
			return true, ""
		}
		return false, "no virtualization detected"
	case "no":
		if got == "" {
			return true, ""
		}
		return false, fmt.Sprintf("virtualization %q detected", got)
	default:
		if got == param {
			return true, ""
		}
		if got == "" {
			return false, fmt.Sprintf("expected %q, no virt detected", param)
		}
		return false, fmt.Sprintf("expected %q, got %q", param, got)
	}
}

// detectVirtualization returns a short identifier of the detected
// virtualization, or "" for bare metal. The probes are best-effort and
// scoped at what is reliably available without root: cpuinfo flag,
// DMI strings, /proc/1/sched, /proc/version. Container detection
// (lxc/docker/podman) reads /proc/1/cgroup.
func detectVirtualization() string {
	// Container probes first (cheap, no DMI access).
	if v := detectContainer(); v != "" {
		return v
	}
	// Hypervisor flag in cpuinfo: set by KVM/QEMU/VMware/Hyper-V/Xen.
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		if bytes.Contains(data, []byte("hypervisor")) {
			// Refine via DMI if available.
			if v := dmiVirt(); v != "" {
				return v
			}
			return "vm"
		}
	}
	// WSL fingerprint.
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s := strings.ToLower(string(data))
		if strings.Contains(s, "microsoft") {
			return "wsl"
		}
	}
	return ""
}

func detectContainer() string {
	if v := os.Getenv("container"); v != "" {
		return v
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		switch {
		case strings.Contains(s, "docker"):
			return "docker"
		case strings.Contains(s, "lxc"):
			return "lxc"
		case strings.Contains(s, "kubepods"):
			return "kubernetes"
		case strings.Contains(s, "podman"):
			return "podman"
		}
	}
	return ""
}

func dmiVirt() string {
	dmi := func(file string) string {
		b, err := os.ReadFile(filepath.Join("/sys/class/dmi/id", file))
		if err != nil {
			return ""
		}
		return strings.ToLower(strings.TrimSpace(string(b)))
	}
	vendor := dmi("sys_vendor")
	product := dmi("product_name")
	switch {
	case strings.Contains(vendor, "qemu"), strings.Contains(product, "qemu"):
		return "qemu"
	case strings.Contains(vendor, "vmware"), strings.Contains(product, "vmware"):
		return "vmware"
	case strings.Contains(vendor, "innotek"), strings.Contains(product, "virtualbox"):
		return "virtualbox"
	case strings.Contains(vendor, "microsoft"), strings.Contains(product, "virtual machine"):
		return "microsoft"
	case strings.Contains(vendor, "xen"):
		return "xen"
	case strings.Contains(vendor, "google"):
		return "google"
	}
	return "vm"
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
		if _, err := os.Stat("/sys/fs/selinux"); err == nil {
			return true, ""
		}
	case "apparmor":
		if _, err := os.Stat("/sys/kernel/security/apparmor"); err == nil {
			return true, ""
		}
	case "tomoyo":
		if _, err := os.Stat("/sys/kernel/security/tomoyo"); err == nil {
			return true, ""
		}
	case "smack":
		if _, err := os.Stat("/sys/fs/smackfs"); err == nil {
			return true, ""
		}
	case "ima":
		if _, err := os.Stat("/sys/kernel/security/ima"); err == nil {
			return true, ""
		}
	case "audit":
		if data, err := os.ReadFile("/sys/kernel/security/lsm"); err == nil {
			if strings.Contains(string(data), "audit") {
				return true, ""
			}
		}
	case "measured-os":
		// systemd v261: succeeds when the kernel has extended TPM PCRs
		// during boot. We look at the TPM binary_bios_measurements log —
		// a non-empty file means firmware/kernel measured something into
		// the TPM. Does not require systemd-stub or UKI.
		st, err := os.Stat("/sys/kernel/security/tpm0/binary_bios_measurements")
		if err != nil {
			return false, "no TPM binary_bios_measurements log"
		}
		if st.Size() == 0 {
			return false, "TPM binary_bios_measurements log is empty"
		}
		return true, ""
	default:
		return false, fmt.Sprintf("unknown security framework %q", param)
	}
	return false, fmt.Sprintf("security framework %q not active", param)
}

// checkNeedsUpdate looks for systemd-style markers under /var/lib (and
// /etc when /var lags behind /usr in mtime). slinit's lightweight read:
// the presence of /run/systemd/update-on-next-boot OR /run/needs-update
// indicates an update is pending. param "yes" succeeds when present.
func checkNeedsUpdate(param string) (bool, string) {
	pending := false
	if _, err := os.Stat("/run/systemd/update-on-next-boot"); err == nil {
		pending = true
	} else if _, err := os.Stat("/run/needs-update"); err == nil {
		pending = true
	}
	switch strings.TrimSpace(param) {
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
	entries, err := os.ReadDir("/sys/class/power_supply")
	if err != nil || len(entries) == 0 {
		return false, false
	}
	// If any AC adapter reports online=1, we're on AC.
	for _, e := range entries {
		typPath := filepath.Join("/sys/class/power_supply", e.Name(), "type")
		t, err := os.ReadFile(typPath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(t)) == "Mains" {
			online, err := os.ReadFile(filepath.Join("/sys/class/power_supply", e.Name(), "online"))
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
		typPath := filepath.Join("/sys/class/power_supply", e.Name(), "type")
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
