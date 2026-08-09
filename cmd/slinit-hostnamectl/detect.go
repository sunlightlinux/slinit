// detect.go — chassis + virtualization detection.
//
// Chassis follows systemd-hostnamed's DMI mapping: read the SMBIOS
// chassis type from /sys/class/dmi/id/chassis_type and translate it
// to the systemd name set {desktop, laptop, server, tablet, handset,
// convertible, watch, embedded, vm, container}.
//
// Virtualization is a best-effort scan: container hints in /proc, then
// hypervisor hints via DMI and CPUID leaves exposed by /proc/cpuinfo.
// Returns "none" when nothing matches, matching systemd's default.

package main

import (
	"os"
	"strconv"
	"strings"
)

// detectChassis reads DMI chassis_type and maps SMBIOS numbers to
// systemd's chassis name set. When mapping fails, falls back to VM /
// container detection so a virtualized guest reports as "vm" or
// "container" rather than the almost-always-"other" DMI default.
func detectChassis() string {
	if v := detectVirt(); v != "" && v != "none" {
		if isContainer(v) {
			return "container"
		}
		return "vm"
	}
	raw := readTrimmed(dmiChassisTypePath)
	if raw == "" {
		return ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return ""
	}
	return chassisFromSMBIOS(n)
}

// chassisFromSMBIOS maps SMBIOS 3.4.0 chassis_type values to systemd's
// chassis vocabulary. Unknown / vague codes (Other=1, Unknown=2) yield
// an empty string so the field is omitted rather than mislabelled.
func chassisFromSMBIOS(n int) string {
	switch n {
	case 3, 4, 5, 6, 7, 13, 15, 24, 25:
		// Desktop, Low-Profile, Pizza Box, Mini Tower, Tower,
		// All-in-One, Space-saving, Sealed-case PC, Multi-system.
		return "desktop"
	case 8, 9, 10, 14:
		// Portable, Laptop, Notebook, Sub Notebook.
		return "laptop"
	case 11:
		return "handset"
	case 17, 23, 28, 29:
		// Main Server Chassis, Rack Mount, Blade, Blade Enclosure.
		return "server"
	case 30:
		return "tablet"
	case 31, 32:
		// Convertible, Detachable.
		return "convertible"
	}
	return ""
}

// detectVirt returns a systemd-style virtualization ID or "none".
// Order matters: container tests come first because a container inside
// a VM should still be identified as a container (matches
// systemd-detect-virt --container preference).
func detectVirt() string {
	if v := detectContainer(); v != "" {
		return v
	}
	if v := detectVM(); v != "" {
		return v
	}
	return "none"
}

// detectContainer inspects /proc/1/environ for container= and
// /proc/self/cgroup / /proc/1/cgroup for cgroup-name hints. Matches
// what systemd-detect-virt --container looks at for the common cases.
func detectContainer() string {
	if v := envValue(proc1EnvironPath, "container"); v != "" {
		return v
	}
	if b, err := os.ReadFile(proc1CgroupPath); err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "docker"):
			return "docker"
		case strings.Contains(s, "lxc"):
			return "lxc"
		case strings.Contains(s, "machine.slice"):
			return "systemd-nspawn"
		case strings.Contains(s, "podman"):
			return "podman"
		}
	}
	// Explicit marker files.
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return "podman"
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	return ""
}

// detectVM checks DMI and /proc/cpuinfo for hypervisor identifiers.
// The DMI table is the primary source; falling back to cpuinfo lets
// us catch minimal or DMI-less hypervisors.
func detectVM() string {
	sysVendor := strings.ToLower(readTrimmed(dmiSysVendorPath))
	product := strings.ToLower(readTrimmed(dmiProductNamePath))
	bios := strings.ToLower(readTrimmed(dmiBiosVendorPath))
	for _, hit := range []struct{ needle, id string }{
		{"kvm", "kvm"},
		{"qemu", "qemu"},
		{"bochs", "bochs"},
		{"vmware", "vmware"},
		{"virtualbox", "oracle"},
		{"innotek", "oracle"},
		{"xen", "xen"},
		{"parallels", "parallels"},
		{"microsoft corporation", "microsoft"},
		{"hyper-v", "microsoft"},
		{"amazon ec2", "amazon"},
	} {
		if strings.Contains(sysVendor, hit.needle) ||
			strings.Contains(product, hit.needle) ||
			strings.Contains(bios, hit.needle) {
			return hit.id
		}
	}
	// /proc/cpuinfo flags contain "hypervisor" whenever the CPU is
	// exposed under one, even without DMI hints.
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		if strings.Contains(string(b), "hypervisor") {
			return "unknown"
		}
	}
	return ""
}

// isContainer returns true when the given detectVirt result names a
// container runtime rather than a hypervisor.
func isContainer(v string) bool {
	switch v {
	case "docker", "podman", "lxc", "systemd-nspawn", "openvz", "rkt", "wsl":
		return true
	}
	return false
}

// envValue returns the value of ENV_KEY in a NUL-separated /proc
// environ file, or "" if absent. Never errors; missing file → "".
func envValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, e := range strings.Split(string(data), "\x00") {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

// Paths overridable in tests.
var (
	proc1EnvironPath = "/proc/1/environ"
	proc1CgroupPath  = "/proc/1/cgroup"
)
