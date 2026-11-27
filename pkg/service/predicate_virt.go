package service

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Virtualization detection, ported from systemd's src/basic/virt.c so
// that condition-virtualization gives the answer ConditionVirtualization=
// gives. Every host path goes through hostPath, and the CPUID probe and
// the architecture through virtCPUID and predicateGOARCH, so tests can
// stand in for any machine.
//
// The ids are systemd's (virtualization_to_string). Differences slinit
// keeps for configs written before 4.1 are handled in
// checkVirtualization, not here: "qemu" also matches a KVM guest whose
// DMI says QEMU (slinit reported KVM guests as "qemu"), "virtualbox"
// matches "oracle", a $container value is reported verbatim rather than
// folded into "container-other", and /proc/1/cgroup still names
// docker, lxc, kubernetes and podman containers that nothing else
// identifies.

// virtVMIDs are systemd's VM ids (VIRTUALIZATION_VM_FIRST..LAST).
var virtVMIDs = map[string]bool{
	"kvm": true, "amazon": true, "qemu": true, "bochs": true, "xen": true, "uml": true,
	"vmware": true, "oracle": true, "microsoft": true, "zvm": true, "parallels": true,
	"bhyve": true, "qnx": true, "acrn": true, "powervm": true, "apple": true, "sre": true,
	"google": true, "vm-other": true,
}

// virtContainerIDs are systemd's container ids (container_table), the
// values of $container it recognises; any other is "container-other".
var virtContainerIDs = map[string]bool{
	"lxc": true, "lxc-libvirt": true, "systemd-nspawn": true, "docker": true, "podman": true,
	"rkt": true, "wsl": true, "proot": true, "pouch": true,
}

// checkVirtualization matches param against the detected
// virtualization, as systemd's condition_test_virtualization:
//
//	"" or "yes"     → any virtualization is detected
//	"no"            → none is
//	"vm"            → a VM id is detected
//	"container"     → a container is detected
//	"private-users" → slinit runs in a user namespace other than the
//	                  initial one
//	an id           → that id is detected ("kvm", "qemu", "docker", ...)
func checkVirtualization(param string) (bool, string) {
	if param == "private-users" {
		if runningInUserns() {
			return true, ""
		}
		return false, "not in a user namespace"
	}
	got := detectVirtualization()
	describe := func() string {
		if got == "" {
			return "no virtualization detected"
		}
		return fmt.Sprintf("virtualization %q detected", got)
	}
	isVM := virtVMIDs[got]
	isContainer := got != "" && !isVM
	var ok bool
	switch param {
	case "", "yes":
		ok = got != ""
	case "no":
		ok = got == ""
	case "vm":
		ok = isVM
	case "container":
		ok = isContainer
	case "container-other":
		// $container is reported verbatim; systemd folds a value it
		// does not know into container-other.
		ok = isContainer && !virtContainerIDs[got]
	case "qemu":
		// slinit before 4.1 reported KVM guests as "qemu" (it went by
		// DMI only); keep those configs matching.
		ok = got == "qemu" || (got == "kvm" && dmiSaysQEMU())
	case "virtualbox":
		// slinit's name before 4.1 for what systemd calls "oracle".
		ok = got == "oracle"
	default:
		ok = got == param
	}
	if ok {
		return true, ""
	}
	if got == "" && param != "" && param != "yes" {
		return false, fmt.Sprintf("expected %q, no virt detected", param)
	}
	if param == "" || param == "yes" || param == "no" {
		return false, describe()
	}
	return false, fmt.Sprintf("expected %q, got %q", param, got)
}

// detectVirtualization returns systemd's id of the detected
// virtualization, or "" for none: a container first, then a VM, as
// detect_virtualization.
func detectVirtualization() string {
	if v := detectContainer(); v != "" {
		return v
	}
	return detectVM()
}

func hostExists(p string) bool {
	_, err := os.Lstat(hostPath(p))
	return err == nil
}

// hostFirstLine is read_one_line_file: the first line, without its
// newline, and whether the file could be read.
func hostFirstLine(p string) (string, bool) {
	data, err := os.ReadFile(hostPath(p))
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimRight(line, "\r"), true
}

// hostProcField is get_proc_field: the first word after "key:" in a
// /proc style file.
func hostProcField(p, key string) (string, bool) {
	data, err := os.ReadFile(hostPath(p))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, key+":"); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return "", true
			}
			return f[0], true
		}
	}
	return "", false
}

// detectContainer is systemd's detect_container. Before it, slinit
// checked $container and /proc/1/cgroup only, and found WSL (whose
// kernel is a VM guest) after the hypervisor probe, so WSL2 read as a
// VM, and Docker on cgroup v2 (no "docker" in /proc/1/cgroup) as
// nothing at all.
func detectContainer() string {
	// /proc/vz exists in and outside an OpenVZ container, /proc/bc
	// only outside.
	if hostExists("/proc/vz") && !hostExists("/proc/bc") {
		return "openvz"
	}
	// Microsoft's documented way of detecting WSL.
	if o, ok := hostFirstLine("/proc/sys/kernel/osrelease"); ok &&
		(strings.Contains(o, "Microsoft") || strings.Contains(o, "WSL")) {
		return "wsl"
	}
	// proot does not use a PID namespace: it ptraces its children.
	if tp, ok := hostProcField("/proc/self/status", "TracerPid"); ok && tp != "" && tp != "0" {
		if _, err := strconv.Atoi(tp); err == nil {
			if comm, ok := hostFirstLine("/proc/" + tp + "/comm"); ok && strings.HasPrefix(comm, "proot") {
				return "proot"
			}
		}
	}
	// What the container manager left for its payload to read.
	if m, ok := hostFirstLine("/run/host/container-manager"); ok && m != "" {
		return translateContainer(m)
	}
	// $container is authoritative for PID 1 in systemd. slinit reads its
	// own environment whether it is PID 1 or not, as it always has, and
	// takes an empty value as unset.
	if e := os.Getenv("container"); e != "" {
		return translateContainer(e)
	}
	if os.Getpid() != 1 {
		// PID 1 may have recorded it, or have it in its environment.
		if m, ok := hostFirstLine("/run/systemd/container"); ok && m != "" {
			return translateContainer(m)
		}
		if data, err := os.ReadFile(hostPath("/proc/1/environ")); err == nil {
			for _, kv := range bytes.Split(data, []byte{0}) {
				if v, ok := bytes.CutPrefix(kv, []byte("container=")); ok && len(v) > 0 {
					return translateContainer(string(v))
				}
			}
		}
	}
	if v := detectContainerFiles(); v != "" {
		return v
	}
	// slinit's own probe, older than the ones above: a container
	// manager's name in PID 1's cgroup path. systemd dropped it; slinit
	// keeps it so the ids it found ("kubernetes" among them) still are.
	if data, err := os.ReadFile(hostPath("/proc/1/cgroup")); err == nil {
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
	// The initial PID namespace has inode 0xEFFFFFFC (since Linux 3.8);
	// any other one is a container of a manager nothing above named.
	if link, err := os.Readlink(hostPath("/proc/self/ns/pid")); err == nil &&
		link != fmt.Sprintf("pid:[%d]", uint32(0xEFFFFFFC)) {
		return "container-other"
	}
	return ""
}

// detectContainerFiles: files container managers drop into the root of
// the payload. Docker's is checked last, and only after the managers
// that name themselves, as an image may carry a stray /.dockerenv.
func detectContainerFiles() string {
	if hostExists("/run/.containerenv") {
		return "podman"
	}
	if hostExists("/.dockerenv") {
		return "docker"
	}
	return ""
}

// translateContainer: "oci" names no manager, so the files decide;
// any other value is the id, verbatim (see the top of the file).
func translateContainer(e string) string {
	if e == "oci" {
		if v := detectContainerFiles(); v != "" {
			return v
		}
		return "container-other"
	}
	return e
}

// runningInUserns is running_in_userns: the initial user namespace has
// inode 0xEFFFFFFD. An unreadable link answers no.
func runningInUserns() bool {
	link, err := os.Readlink(hostPath("/proc/self/ns/user"))
	if err != nil {
		return false
	}
	return link != fmt.Sprintf("user:[%d]", uint32(0xEFFFFFFD))
}

func virtArchIsX86() bool { return predicateGOARCH == "amd64" || predicateGOARCH == "386" }

// virtDMIVendors is systemd's dmi_vendor_table: a prefix of one of the
// DMI strings, in this order, names the VM.
var virtDMIVendors = []struct{ prefix, id string }{
	{"KVM", "kvm"},
	{"OpenStack", "kvm"},
	{"KubeVirt", "kvm"},
	{"Alibaba Cloud ECS", "kvm"},
	{"Amazon EC2", "amazon"},
	{"QEMU", "qemu"},
	{"VMware", "vmware"},
	{"VMW", "vmware"},
	{"innotek GmbH", "oracle"},
	{"VirtualBox", "oracle"},
	{"Oracle Corporation", "oracle"},
	{"Xen", "xen"},
	{"Bochs", "bochs"},
	{"Parallels", "parallels"},
	{"BHYVE", "bhyve"},
	{"Hyper-V", "microsoft"},
	{"Apple Virtualization", "apple"},
	{"Google Compute Engine", "google"},
}

// virtDMIFiles: product_name before sys_vendor, to see KVM before QEMU.
var virtDMIFiles = []string{"product_name", "sys_vendor", "board_vendor", "bios_vendor", "product_version"}

func detectVMDMIVendor() string {
	for _, f := range virtDMIFiles {
		s, ok := hostFirstLine("/sys/class/dmi/id/" + f)
		if !ok {
			continue
		}
		for _, v := range virtDMIVendors {
			if strings.HasPrefix(s, v.prefix) {
				return v.id
			}
		}
	}
	return ""
}

// dmiSaysQEMU: the DMI strings name QEMU (see checkVirtualization).
func dmiSaysQEMU() bool {
	for _, f := range virtDMIFiles {
		if s, ok := hostFirstLine("/sys/class/dmi/id/" + f); ok && strings.HasPrefix(s, "QEMU") {
			return true
		}
	}
	return false
}

const (
	smbiosVMBitSet = iota
	smbiosVMBitUnset
	smbiosVMBitUnknown
)

// detectVMSMBIOS reads bit 4 of BIOS Characteristics Extension Byte 2,
// which firmware sets in a VM. Unset does not mean bare metal.
func detectVMSMBIOS() int {
	b, err := os.ReadFile(hostPath("/sys/firmware/dmi/entries/0-0/raw"))
	if err != nil || len(b) < 20 || b[1] < 20 {
		return smbiosVMBitUnknown
	}
	if b[19]&(1<<4) != 0 {
		return smbiosVMBitSet
	}
	return smbiosVMBitUnset
}

// detectVMDMI is detect_vm_dmi: DMI on the architectures that have it,
// with the Amazon EC2 metal-instance exception.
func detectVMDMI() string {
	switch predicateGOARCH {
	case "386", "amd64", "arm", "arm64", "loong64", "riscv64":
	default:
		return ""
	}
	r := detectVMDMIVendor()
	if r == "amazon" {
		switch detectVMSMBIOS() {
		case smbiosVMBitSet:
			return "amazon"
		case smbiosVMBitUnset:
			return ""
		default:
			s, ok := hostFirstLine("/sys/class/dmi/id/product_name")
			if !ok {
				return "amazon"
			}
			if _, after, found := strings.Cut(s, ".metal"); found && (after == "" || after[0] == '-') {
				return ""
			}
			return "amazon"
		}
	}
	if r == "" && detectVMSMBIOS() == smbiosVMBitSet {
		return "vm-other"
	}
	return r
}

// detectVMUML: User-Mode Linux names itself in the first vendor_id.
func detectVMUML() string {
	data, err := os.ReadFile(hostPath("/proc/cpuinfo"))
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if t, ok := strings.CutPrefix(sc.Text(), "vendor_id\t: "); ok {
			if strings.HasPrefix(t, "User Mode Linux") {
				return "uml"
			}
			break
		}
	}
	return ""
}

// detectVMXenDom0: the hardware domain is the host, not a guest.
func detectVMXenDom0() bool {
	if s, ok := hostFirstLine("/sys/hypervisor/properties/features"); ok {
		if f, err := strconv.ParseUint(strings.TrimSpace(s), 16, 64); err == nil {
			return f&(1<<11) != 0 // XENFEAT_dom0
		}
	}
	s, ok := hostFirstLine("/proc/xen/capabilities")
	if !ok {
		return false
	}
	for _, c := range strings.Split(s, ",") {
		if c == "control_d" {
			return true
		}
	}
	return false
}

// virtCPUIDSigs is systemd's vm_table for the CPUID vendor signature.
var virtCPUIDSigs = map[string]string{
	"XenVMMXenVMM": "xen",
	"KVMKVMKVM":    "kvm",
	"Linux KVM Hv": "kvm",
	"TCGTCGTCGTCG": "qemu",
	"VMwareVMware": "vmware",
	"Microsoft Hv": "microsoft",
	"bhyve bhyve ": "bhyve",
	"QNXQVMBSQG":   "qnx",
	"ACRNACRNACRN": "acrn",
	"SRESRESRESRE": "sre",
	"Apple VZ":     "apple",
}

func detectVMCPUID() string {
	if !virtArchIsX86() || virtCPUID == nil {
		return ""
	}
	hv, sig := virtCPUID()
	if !hv {
		return ""
	}
	if id, ok := virtCPUIDSigs[sig]; ok {
		return id
	}
	return "vm-other"
}

func detectVMHypervisor() string {
	t, ok := hostFirstLine("/sys/hypervisor/type")
	if !ok {
		return ""
	}
	if t == "xen" {
		return "xen"
	}
	return "vm-other"
}

func detectVMDeviceTree() string {
	switch predicateGOARCH {
	case "arm", "arm64", "ppc64", "ppc64le", "riscv64":
	default:
		return ""
	}
	hv, ok := hostFirstLine("/proc/device-tree/hypervisor/compatible")
	if !ok {
		if hostExists("/proc/device-tree/ibm,partition-name") &&
			hostExists("/proc/device-tree/hmc-managed?") &&
			!hostExists("/proc/device-tree/chosen/qemu,graphic-width") {
			return "powervm"
		}
		entries, err := os.ReadDir(hostPath("/proc/device-tree"))
		if err != nil {
			return ""
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), "fw-cfg") {
				return "qemu"
			}
		}
		// read_one_line_file stops at the first NUL-free line; the
		// device-tree string is NUL-terminated.
		if c, ok := hostFirstLine("/proc/device-tree/compatible"); ok {
			c = strings.TrimRight(c, "\x00")
			if i := strings.IndexByte(c, 0); i >= 0 {
				c = c[:i]
			}
			switch c {
			case "qemu,pseries":
				return "qemu"
			case "linux,dummy-virt":
				return "vm-other"
			}
		}
		return ""
	}
	hv = strings.TrimRight(hv, "\x00")
	switch {
	case hv == "linux,kvm":
		return "kvm"
	case strings.Contains(hv, "xen"):
		return "xen"
	case strings.Contains(hv, "vmware"):
		return "vmware"
	}
	return "vm-other"
}

func detectVMZVM() string {
	if predicateGOARCH != "s390x" {
		return ""
	}
	t, ok := hostProcField("/proc/sysinfo", "VM00 Control Program")
	if !ok {
		return ""
	}
	if t == "z/VM" {
		return "zvm"
	}
	return "kvm"
}

// detectVM is systemd's detect_vm, in its order: the DMI vendors that
// run on top of KVM or cloak as Hyper-V first, then UML and Xen, then
// CPUID (which says KVM whatever DMI claims), then DMI again, the
// hypervisor sysfs node, the device tree and z/VM.
func detectVM() string {
	hyperv, other := false, false
	xenDom0 := false
	dmi := detectVMDMI()
	var v string
	switch {
	case dmi == "oracle", dmi == "xen", dmi == "amazon", dmi == "parallels",
		dmi == "google" && !virtArchIsX86():
		v = dmi
		goto finish
	}
	if v = detectVMUML(); v != "" {
		goto finish
	}
	if hostExists("/proc/xen") {
		v = "xen"
		// Dom0 is not a guest, unless it is nested in a hypervisor
		// the CPUID probe below can see.
		xenDom0 = detectVMXenDom0()
		if !xenDom0 {
			goto finish
		}
		v = ""
	}
	v = detectVMCPUID()
	switch {
	case v == "microsoft":
		// QEMU with Hyper-V enlightenments shows Microsoft's
		// signature; let the other probes name it first.
		hyperv = true
		v = ""
	case v == "vm-other":
		other = true
		v = ""
	case v == "kvm" && dmi == "google":
		return "google"
	case v != "":
		goto finish
	}
	if xenDom0 {
		goto finish
	}
	if dmi == "vm-other" {
		other = true
	} else if dmi != "" && dmi != "google" {
		v = dmi
		goto finish
	}
	if v = detectVMHypervisor(); v == "vm-other" {
		other = true
		v = ""
	} else if v != "" {
		goto finish
	}
	if v = detectVMDeviceTree(); v == "vm-other" {
		other = true
		v = ""
	} else if v != "" {
		goto finish
	}
	v = detectVMZVM()
finish:
	if v == "" {
		if hyperv {
			v = "microsoft"
		} else if other {
			v = "vm-other"
		}
	}
	return v
}
