package service

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests in this file point predicateRoot at a fixture tree, so the
// machine predicates read /proc, /sys, /etc and /run files the test
// wrote instead of the host's. None of them may run in parallel.

// withPredicateRoot points the fixed host paths at a fresh fixture
// root for the length of the test and returns that root.
//
// The probes that do not read files are faked too, as the fixture
// machine's: x86-64, no hypervisor in CPUID, and a kernel without
// audit. A test that needs another machine sets the variable itself.
func withPredicateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origRoot, origArch, origCPUID, origAudit := predicateRoot, predicateGOARCH, virtCPUID, auditProbe
	predicateRoot = root
	predicateGOARCH = "amd64"
	virtCPUID = func() (bool, string) { return false, "" }
	auditProbe = func() (error, error) { return syscall.EPROTONOSUPPORT, nil }
	t.Cleanup(func() {
		predicateRoot, predicateGOARCH, virtCPUID, auditProbe = origRoot, origArch, origCPUID, origAudit
	})
	return root
}

// put writes content at the host path p under root, creating parents.
func put(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mkdir creates the host directory p under root.
func mkdir(t *testing.T, root, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
		t.Fatal(err)
	}
}

// predCase is one Evaluate call and the answer it must give.
type predCase struct {
	param  string
	negate bool
	want   bool
}

// runPred evaluates every case through Predicate.Evaluate, so the
// evalRaw dispatch and the negation are part of what is tested.
func runPred(t *testing.T, kind PredicateKind, cases []predCase) {
	t.Helper()
	for _, c := range cases {
		p := Predicate{Kind: kind, Param: c.param, Negate: c.negate}
		got, why := p.Evaluate()
		if got != c.want {
			t.Errorf("%s = %v (%q), want %v", p.String(), got, why, c.want)
		}
		if !got && why == "" {
			t.Errorf("%s failed without a reason", p.String())
		}
	}
}

// -------- kernel-command-line --------------------------------------------

func TestKernelCommandLinePredicate(t *testing.T) {
	root := withPredicateRoot(t)
	put(t, root, "/proc/cmdline",
		"BOOT_IMAGE=/vmlinuz quiet console=ttyS0,115200 rd.break foo=bar=baz\n")
	runPred(t, PredKernelCommandLine, []predCase{
		{"quiet", false, true},
		{" quiet ", false, true},
		{"rd.break", false, true},
		// A bare key matches key=anything, as in systemd.
		{"console", false, true},
		{"BOOT_IMAGE", false, true},
		// key=value matches the whole word only.
		{"console=ttyS0,115200", false, true},
		{"console=tty0", false, false},
		{"foo=bar", false, false},
		{"foo=bar=baz", false, true},
		// Neither a prefix nor a value of a word is a word.
		{"quie", false, false},
		{"rd", false, false},
		{"ttyS0,115200", false, false},
		{"cons", false, false},
		{"nosuch", false, false},
		{"quiet", true, false},
		{"nosuch", true, true},
	})
}

func TestKernelCommandLineMissingFile(t *testing.T) {
	withPredicateRoot(t)
	ok, why := kernelCmdlineContains("quiet")
	if ok || !strings.Contains(why, "/proc/cmdline") {
		t.Fatalf("missing /proc/cmdline = %v %q, want false naming the file", ok, why)
	}
}

// -------- virtualization --------------------------------------------------

// unsetContainerEnv removes $container for the test, which t.Setenv
// cannot do (it can only set a value, and an empty one is a value).
func unsetContainerEnv(t *testing.T) {
	t.Helper()
	t.Setenv("container", "") // registers the restore
	os.Unsetenv("container")
}

// hv is a CPUID probe that reports a hypervisor with signature sig.
func hv(sig string) func() (bool, string) {
	return func() (bool, string) { return true, sig }
}

// smbiosRaw is a BIOS information structure (DMI type 0) with the "VM"
// bit of BIOS Characteristics Extension Byte 2 set or not.
func smbiosRaw(vm bool) string {
	b := make([]byte, 24)
	b[1] = 24
	if vm {
		b[19] = 1 << 4
	}
	return string(b)
}

func TestVirtualizationDetect(t *testing.T) {
	const kvmSig, tcgSig = "KVMKVMKVM", "TCGTCGTCGTCG"
	for _, tc := range []struct {
		name  string
		env   string // "-" leaves $container unset
		arch  string // GOARCH, default amd64
		cpuid func() (bool, string)
		files map[string]string
		want  string
	}{
		{name: "bare metal", env: "-", files: map[string]string{
			"/proc/cpuinfo":              "vendor_id\t: GenuineIntel\nflags\t\t: fpu vme sse2\n",
			"/proc/sys/kernel/osrelease": "6.12.0-sunlight\n",
			"/proc/1/cgroup":             "0::/init.scope\n",
			"/proc/self/ns/pid":          "",
		}, want: ""},
		{name: "nothing readable", env: "-", want: ""},
		{name: "empty env is unset", env: "", want: ""},

		// Containers.
		{name: "container env", env: "systemd-nspawn", want: "systemd-nspawn"},
		{name: "unknown env verbatim", env: "garden", want: "garden"},
		{name: "env beats cgroup", env: "lxc", files: map[string]string{"/proc/1/cgroup": "0::/docker/abc\n"}, want: "lxc"},
		{name: "oci env, dockerenv", env: "oci", files: map[string]string{"/.dockerenv": ""}, want: "docker"},
		{name: "oci env, nothing else", env: "oci", want: "container-other"},
		{name: "container-manager file", env: "-", files: map[string]string{"/run/host/container-manager": "podman\n"}, want: "podman"},
		{name: "run systemd container", env: "-", files: map[string]string{"/run/systemd/container": "lxc-libvirt\n"}, want: "lxc-libvirt"},
		{name: "pid 1 environ", env: "-", files: map[string]string{"/proc/1/environ": "HOME=/\x00container=rkt\x00TERM=linux\x00"}, want: "rkt"},
		// Docker on cgroup v2: PID 1's cgroup is "0::/", only
		// /.dockerenv says docker.
		{name: "dockerenv cgroup v2", env: "-", files: map[string]string{"/.dockerenv": "", "/proc/1/cgroup": "0::/\n"}, want: "docker"},
		{name: "containerenv beats dockerenv", env: "-", files: map[string]string{"/.dockerenv": "", "/run/.containerenv": ""}, want: "podman"},
		{name: "docker cgroup", env: "-", files: map[string]string{"/proc/1/cgroup": "0::/docker/abc\n"}, want: "docker"},
		{name: "lxc cgroup", env: "-", files: map[string]string{"/proc/1/cgroup": "0::/lxc/c1\n"}, want: "lxc"},
		{name: "kubepods cgroup", env: "-", files: map[string]string{"/proc/1/cgroup": "0::/kubepods/burstable/p1\n"}, want: "kubernetes"},
		{name: "podman cgroup", env: "-", files: map[string]string{"/proc/1/cgroup": "0::/machine.slice/libpod-podman-1\n"}, want: "podman"},
		{name: "openvz", env: "-", files: map[string]string{"/proc/vz/veinfo": ""}, want: "openvz"},
		{name: "openvz host", env: "-", files: map[string]string{"/proc/vz/veinfo": "", "/proc/bc/0": ""}, want: ""},
		{name: "proot", env: "-", files: map[string]string{
			"/proc/self/status": "Name:\tsh\nTracerPid:\t4242\n",
			"/proc/4242/comm":   "proot\n",
		}, want: "proot"},
		{name: "traced by gdb", env: "-", files: map[string]string{
			"/proc/self/status": "Name:\tsh\nTracerPid:\t4242\n",
			"/proc/4242/comm":   "gdb\n",
		}, want: ""},
		{name: "container beats hypervisor", env: "-", cpuid: hv(kvmSig), files: map[string]string{
			"/proc/1/cgroup": "0::/docker/abc\n",
		}, want: "docker"},
		// WSL2 runs on Hyper-V: its kernel sees a hypervisor, and it
		// read as a VM when the WSL fingerprint came after that.
		{name: "wsl2", env: "-", cpuid: hv("Microsoft Hv"), files: map[string]string{
			"/proc/sys/kernel/osrelease":   "5.15.90.1-microsoft-standard-WSL2\n",
			"/sys/class/dmi/id/sys_vendor": "Microsoft Corporation\n",
		}, want: "wsl"},
		{name: "wsl1", env: "-", files: map[string]string{
			"/proc/sys/kernel/osrelease": "4.4.0-19041-Microsoft\n",
		}, want: "wsl"},
		{name: "microsoft-built kernel is not wsl", env: "-", files: map[string]string{
			"/proc/sys/kernel/osrelease": "6.6.0-microsoft-cbl\n",
		}, want: ""},

		// VMs by CPUID.
		{name: "kvm", env: "-", cpuid: hv(kvmSig), files: map[string]string{
			"/sys/class/dmi/id/sys_vendor":   "QEMU\n",
			"/sys/class/dmi/id/product_name": "Standard PC (Q35 + ICH9, 2009)\n",
		}, want: "kvm"},
		{name: "kvm hyperv enlightened", env: "-", cpuid: hv("Linux KVM Hv"), want: "kvm"},
		{name: "qemu tcg", env: "-", cpuid: hv(tcgSig), files: map[string]string{
			"/sys/class/dmi/id/sys_vendor": "QEMU\n",
		}, want: "qemu"},
		{name: "hyper-v", env: "-", cpuid: hv("Microsoft Hv"), files: map[string]string{
			"/sys/class/dmi/id/sys_vendor":   "Microsoft Corporation\n",
			"/sys/class/dmi/id/product_name": "Virtual Machine\n",
		}, want: "microsoft"},
		// QEMU with Hyper-V enlightenments: DMI names it.
		{name: "qemu cloaked as hyper-v", env: "-", cpuid: hv("Microsoft Hv"), files: map[string]string{
			"/sys/class/dmi/id/sys_vendor": "QEMU\n",
		}, want: "qemu"},
		{name: "unknown hypervisor", env: "-", cpuid: hv("AcmeAcmeAcme"), want: "vm-other"},
		{name: "vmware", env: "-", cpuid: hv("VMwareVMware"), want: "vmware"},

		// VMs DMI names before CPUID.
		{name: "virtualbox", env: "-", cpuid: hv(kvmSig), files: map[string]string{
			"/sys/class/dmi/id/sys_vendor": "innotek GmbH\n",
		}, want: "oracle"},
		{name: "gce", env: "-", cpuid: hv(kvmSig), files: map[string]string{
			"/sys/class/dmi/id/product_name": "Google Compute Engine\n",
		}, want: "google"},
		{name: "gce bare metal", env: "-", files: map[string]string{
			"/sys/class/dmi/id/product_name": "Google Compute Engine\n",
		}, want: ""},
		{name: "amazon vm bit", env: "-", cpuid: hv(kvmSig), files: map[string]string{
			"/sys/class/dmi/id/sys_vendor":      "Amazon EC2\n",
			"/sys/firmware/dmi/entries/0-0/raw": smbiosRaw(true),
		}, want: "amazon"},
		{name: "amazon metal", env: "-", files: map[string]string{
			"/sys/class/dmi/id/sys_vendor":      "Amazon EC2\n",
			"/sys/firmware/dmi/entries/0-0/raw": smbiosRaw(false),
		}, want: ""},
		{name: "amazon metal by name", env: "-", files: map[string]string{
			"/sys/class/dmi/id/sys_vendor":   "Amazon EC2\n",
			"/sys/class/dmi/id/product_name": "c5.metal\n",
		}, want: ""},

		// No CPUID hypervisor: DMI alone, as systemd.
		{name: "dmi qemu without cpuid", env: "-", files: map[string]string{
			"/sys/class/dmi/id/sys_vendor": "QEMU\n",
		}, want: "qemu"},
		{name: "dmi kvm product", env: "-", files: map[string]string{
			"/sys/class/dmi/id/product_name": "KVM\n",
			"/sys/class/dmi/id/sys_vendor":   "QEMU\n",
		}, want: "kvm"},
		{name: "smbios vm bit only", env: "-", files: map[string]string{
			"/sys/firmware/dmi/entries/0-0/raw": smbiosRaw(true),
		}, want: "vm-other"},
		{name: "uml", env: "-", files: map[string]string{
			"/proc/cpuinfo": "processor\t: 0\nvendor_id\t: User Mode Linux\n",
		}, want: "uml"},
		{name: "xen domU", env: "-", files: map[string]string{"/proc/xen/capabilities": ""}, want: "xen"},
		// Dom0 is the host, not a guest: none, unless CPUID shows it
		// nested in another hypervisor.
		{name: "xen dom0", env: "-", files: map[string]string{
			"/proc/xen/capabilities":              "",
			"/sys/hypervisor/properties/features": "00000801\n",
			"/sys/hypervisor/type":                "xen\n",
		}, want: ""},
		{name: "xen dom0 by capabilities", env: "-", files: map[string]string{
			"/proc/xen/capabilities": "control_d\n",
		}, want: ""},
		{name: "xen dom0 nested in kvm", env: "-", cpuid: hv(kvmSig), files: map[string]string{
			"/proc/xen/capabilities": "control_d\n",
		}, want: "kvm"},
		{name: "xen hvm domU by sysfs", env: "-", files: map[string]string{
			"/sys/hypervisor/type": "xen\n",
		}, want: "xen"},

		// Other architectures: no CPUID, the device tree.
		{name: "arm64 kvm", env: "-", arch: "arm64", cpuid: hv("KVMKVMKVM"), files: map[string]string{
			"/proc/device-tree/hypervisor/compatible": "linux,kvm\x00",
		}, want: "kvm"},
		{name: "arm64 fw-cfg", env: "-", arch: "arm64", files: map[string]string{
			"/proc/device-tree/fw-cfg@9020000/compatible": "qemu,fw-cfg-mmio\x00",
		}, want: "qemu"},
		{name: "arm64 dummy-virt", env: "-", arch: "arm64", files: map[string]string{
			"/proc/device-tree/compatible": "linux,dummy-virt\x00",
		}, want: "vm-other"},
		{name: "s390x zvm", env: "-", arch: "s390x", files: map[string]string{
			"/proc/sysinfo": "VM00 Name:  LINUX1\nVM00 Control Program: z/VM    7.2.0\n",
		}, want: "zvm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withPredicateRoot(t)
			if tc.env == "-" {
				unsetContainerEnv(t)
			} else {
				t.Setenv("container", tc.env)
			}
			if tc.arch != "" {
				predicateGOARCH = tc.arch
			}
			if tc.cpuid != nil {
				virtCPUID = tc.cpuid
			}
			for p, c := range tc.files {
				put(t, root, p, c)
			}
			if got := detectVirtualization(); got != tc.want {
				t.Fatalf("detectVirtualization() = %q, want %q", got, tc.want)
			}
		})
	}
	// The PID namespace: the initial one has inode 0xEFFFFFFC.
	t.Run("pid namespace", func(t *testing.T) {
		root := withPredicateRoot(t)
		unsetContainerEnv(t)
		mkdir(t, root, "/proc/self/ns")
		link := filepath.Join(root, "/proc/self/ns/pid")
		if err := os.Symlink("pid:[4026531836]", link); err != nil {
			t.Fatal(err)
		}
		if got := detectVirtualization(); got != "" {
			t.Fatalf("initial pid namespace: %q, want none", got)
		}
		os.Remove(link)
		if err := os.Symlink("pid:[4026532999]", link); err != nil {
			t.Fatal(err)
		}
		if got := detectVirtualization(); got != "container-other" {
			t.Fatalf("other pid namespace: %q, want container-other", got)
		}
	})
}

func TestVirtualizationPredicate(t *testing.T) {
	t.Run("bare metal", func(t *testing.T) {
		withPredicateRoot(t)
		unsetContainerEnv(t)
		runPred(t, PredVirtualization, []predCase{
			{"", false, false},
			{"yes", false, false},
			{"no", false, true},
			{"kvm", false, false},
			{"vm", false, false},
			{"container", false, false},
			{"no", true, false},
			{"yes", true, true},
		})
		if _, why := checkVirtualization("kvm"); !strings.Contains(why, "no virt") {
			t.Errorf("reason %q should say no virtualization was detected", why)
		}
	})
	t.Run("kvm", func(t *testing.T) {
		root := withPredicateRoot(t)
		unsetContainerEnv(t)
		virtCPUID = hv("KVMKVMKVM")
		put(t, root, "/sys/class/dmi/id/sys_vendor", "QEMU\n")
		runPred(t, PredVirtualization, []predCase{
			{"", false, true},
			{"yes", false, true},
			{"no", false, false},
			{"kvm", false, true},
			{"vm", false, true},
			{"container", false, false},
			// slinit before 4.1 said "qemu" here, from the DMI.
			{"qemu", false, true},
			{"QEMU", false, false},
			{"kvm", true, false},
			{"no", true, true},
		})
	})
	t.Run("kvm without qemu dmi", func(t *testing.T) {
		withPredicateRoot(t)
		unsetContainerEnv(t)
		virtCPUID = hv("KVMKVMKVM")
		runPred(t, PredVirtualization, []predCase{{"kvm", false, true}, {"qemu", false, false}})
	})
	t.Run("qemu tcg", func(t *testing.T) {
		root := withPredicateRoot(t)
		unsetContainerEnv(t)
		virtCPUID = hv("TCGTCGTCGTCG")
		put(t, root, "/sys/class/dmi/id/sys_vendor", "QEMU\n")
		runPred(t, PredVirtualization, []predCase{{"qemu", false, true}, {"kvm", false, false}, {"vm", false, true}})
	})
	t.Run("virtualbox", func(t *testing.T) {
		root := withPredicateRoot(t)
		unsetContainerEnv(t)
		put(t, root, "/sys/class/dmi/id/sys_vendor", "innotek GmbH\n")
		runPred(t, PredVirtualization, []predCase{{"oracle", false, true}, {"virtualbox", false, true}, {"vm", false, true}})
	})
	t.Run("docker", func(t *testing.T) {
		root := withPredicateRoot(t)
		unsetContainerEnv(t)
		virtCPUID = hv("KVMKVMKVM")
		put(t, root, "/.dockerenv", "")
		runPred(t, PredVirtualization, []predCase{
			{"docker", false, true},
			{"container", false, true},
			{"vm", false, false},
			{"container-other", false, false},
			{"kvm", false, false},
			{"yes", false, true},
		})
	})
	t.Run("unknown container manager", func(t *testing.T) {
		withPredicateRoot(t)
		t.Setenv("container", "garden")
		runPred(t, PredVirtualization, []predCase{
			{"garden", false, true},
			{"container", false, true},
			{"container-other", false, true},
		})
	})
	t.Run("private-users", func(t *testing.T) {
		root := withPredicateRoot(t)
		unsetContainerEnv(t)
		runPred(t, PredVirtualization, []predCase{{"private-users", false, false}})
		mkdir(t, root, "/proc/self/ns")
		link := filepath.Join(root, "/proc/self/ns/user")
		if err := os.Symlink("user:[4026531837]", link); err != nil {
			t.Fatal(err)
		}
		runPred(t, PredVirtualization, []predCase{{"private-users", false, false}, {"private-users", true, true}})
		os.Remove(link)
		if err := os.Symlink("user:[4026532001]", link); err != nil {
			t.Fatal(err)
		}
		// Not a container by itself: only the user namespace differs.
		runPred(t, PredVirtualization, []predCase{{"private-users", false, true}, {"no", false, true}})
	})
}

// TestVirtualizationHostComplement runs on the real host: whatever it
// is, "yes" and "no" must disagree, and a named match must imply "yes".
func TestVirtualizationHostComplement(t *testing.T) {
	yes, _ := checkVirtualization("yes")
	no, _ := checkVirtualization("no")
	if yes == no {
		t.Fatalf("virtualization yes=%v no=%v on this host; they must differ", yes, no)
	}
	if got := detectVirtualization(); got != "" {
		if ok, why := checkVirtualization(got); !ok {
			t.Fatalf("detected %q but checkVirtualization(%q) = false: %s", got, got, why)
		}
	}
}

// -------- security --------------------------------------------------------

func TestSecurityPredicate(t *testing.T) {
	markers := map[string]string{
		"selinux":  "/sys/fs/selinux",
		"apparmor": "/sys/kernel/security/apparmor",
		"tomoyo":   "/sys/kernel/security/tomoyo",
		"smack":    "/sys/fs/smackfs",
		"ima":      "/sys/kernel/security/ima",
	}
	for lsm, dir := range markers {
		t.Run(lsm, func(t *testing.T) {
			root := withPredicateRoot(t)
			runPred(t, PredSecurity, []predCase{{lsm, false, false}, {lsm, true, true}})
			if _, why := checkSecurity(lsm); !strings.Contains(why, "not active") {
				t.Errorf("reason %q should say not active", why)
			}
			mkdir(t, root, dir)
			runPred(t, PredSecurity, []predCase{
				{lsm, false, true},
				{strings.ToUpper(lsm), false, true},
				{lsm, true, false},
			})
			// Only its own marker turns a framework on.
			for other := range markers {
				if other != lsm {
					runPred(t, PredSecurity, []predCase{{other, false, false}})
				}
			}
		})
	}
	t.Run("audit", func(t *testing.T) {
		root := withPredicateRoot(t)
		// Audit is not an LSM: its name in the LSM list (where the
		// kernel never puts it) means nothing.
		put(t, root, "/sys/kernel/security/lsm", "capability,audit\n")
		runPred(t, PredSecurity, []predCase{{"audit", false, false}, {"audit", true, true}})
		auditProbe = func() (error, error) { return nil, nil }
		runPred(t, PredSecurity, []predCase{{"audit", false, true}, {"audit", true, false}})
		// The probe on this kernel: whatever it says, the condition
		// agrees with it.
		auditProbe = probeAuditNetlink
		want := auditUsable(probeAuditNetlink())
		runPred(t, PredSecurity, []predCase{{"audit", false, want}})
	})
	t.Run("measured-os", func(t *testing.T) {
		root := withPredicateRoot(t)
		const log = "/sys/kernel/security/tpm0/binary_bios_measurements"
		const stub = "/sys/firmware/efi/efivars/StubPcrKernelImage-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
		runPred(t, PredSecurity, []predCase{{"measured-os", false, false}})
		// A TPM event log is what any TPM firmware writes; it is not
		// a measured OS.
		put(t, root, log, "\x01\x02")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, false}, {"measured-os", true, true}})
		// A measured UKI is.
		put(t, root, stub, "\x06\x00\x00\x001\x001\x00")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, true}, {"measured-os", true, false}})
		// The kernel command line overrides, either way, the last
		// word winning; a value that is no boolean is ignored.
		for cmdline, want := range map[string]bool{
			"quiet systemd.tpm2_measured_os=0":                      false,
			"systemd.tpm2_measured_os=no":                           false,
			"systemd.tpm2-measured-os=off":                          false,
			"systemd.tpm2_measured_os=0 systemd.tpm2_measured_os=1": true,
			"systemd.tpm2_measured_os=maybe":                        true,
			"systemd.tpm2_measured_os_x=0":                          true,
		} {
			put(t, root, "/proc/cmdline", cmdline+"\n")
			runPred(t, PredSecurity, []predCase{{"measured-os", false, want}})
		}
		if err := os.Remove(filepath.Join(root, stub)); err != nil {
			t.Fatal(err)
		}
		for cmdline, want := range map[string]bool{
			"systemd.tpm2_measured_os":   true,
			"systemd.tpm2_measured_os=1": true,
			"systemd.tpm2_measured_os=y": true,
			"quiet":                      false,
		} {
			put(t, root, "/proc/cmdline", cmdline+"\n")
			runPred(t, PredSecurity, []predCase{{"measured-os", false, want}})
		}
		// $SYSTEMD_FORCE_MEASURE comes before the command line.
		put(t, root, "/proc/cmdline", "systemd.tpm2_measured_os=1\n")
		t.Setenv("SYSTEMD_FORCE_MEASURE", "0")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, false}})
		t.Setenv("SYSTEMD_FORCE_MEASURE", "bogus")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, true}})
		put(t, root, "/proc/cmdline", "quiet\n")
		t.Setenv("SYSTEMD_FORCE_MEASURE", "yes")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, true}})
	})
	t.Run("measured-uki", func(t *testing.T) {
		root := withPredicateRoot(t)
		const log = "/sys/kernel/security/tpm0/binary_bios_measurements"
		const stub = "/sys/firmware/efi/efivars/StubPcrKernelImage-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"
		// A TPM log without the stub variable is measured-os, not -uki.
		put(t, root, log, "\x01")
		runPred(t, PredSecurity, []predCase{{"measured-uki", false, false}})
		put(t, root, stub, "\x06\x00\x00\x00\x0b\x00\x00\x00")
		runPred(t, PredSecurity, []predCase{{"measured-uki", false, true}, {"measured-uki", true, false}})
		put(t, root, log, "")
		runPred(t, PredSecurity, []predCase{{"measured-uki", false, false}})
		if err := os.Remove(filepath.Join(root, log)); err != nil {
			t.Fatal(err)
		}
		runPred(t, PredSecurity, []predCase{{"measured-uki", false, false}})
	})
	t.Run("unknown", func(t *testing.T) {
		root := withPredicateRoot(t)
		for _, dir := range markers {
			mkdir(t, root, dir)
		}
		runPred(t, PredSecurity, []predCase{{"grsecurity", false, false}, {"", false, false}})
		if _, why := checkSecurity("grsecurity"); !strings.Contains(why, "unknown") {
			t.Errorf("reason %q should say unknown", why)
		}
	})
}

// -------- needs-update ----------------------------------------------------

func TestNeedsUpdatePredicate(t *testing.T) {
	root := withPredicateRoot(t)
	runPred(t, PredNeedsUpdate, []predCase{
		{"", false, false},
		{"yes", false, false},
		{"no", false, true},
		{"no", true, false},
	})
	for _, marker := range []string{"/run/needs-update", "/run/systemd/update-on-next-boot"} {
		put(t, root, marker, "")
		runPred(t, PredNeedsUpdate, []predCase{
			{"", false, true},
			{" yes ", false, true},
			{"no", false, false},
			{"yes", true, false},
		})
		if err := os.Remove(filepath.Join(root, marker)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestNeedsUpdateDirectory: systemd's form, an absolute directory, is
// true when /usr was modified after DIR/.updated.
func TestNeedsUpdateDirectory(t *testing.T) {
	root := withPredicateRoot(t)
	mkdir(t, root, "/usr")
	mkdir(t, root, "/etc")
	stamp := filepath.Join(root, "/etc/.updated")
	usr := filepath.Join(root, "/usr")
	at := func(p string, sec, nsec int64) {
		t.Helper()
		tm := time.Unix(sec, nsec)
		if err := os.Chtimes(p, tm, tm); err != nil {
			t.Fatal(err)
		}
	}
	// No stamp yet: never updated, so update.
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}, {"/etc", true, false}})
	put(t, root, "/etc/.updated", "")
	at(usr, 1000, 0)
	at(stamp, 2000, 0)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, false}, {" /etc ", false, false}, {"/etc", true, true}})
	at(usr, 3000, 0)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}})
	// Same second: the nanoseconds decide.
	at(usr, 3000, 500)
	at(stamp, 3000, 400)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}})
	at(stamp, 3000, 600)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, false}})
	// A stamp without nanoseconds (its filesystem keeps none): the
	// TIMESTAMP_NSEC= it records decides.
	put(t, root, "/etc/.updated", "# written by systemd-update-done\nTIMESTAMP_NSEC=3000000000600\n")
	at(stamp, 3000, 0)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, false}})
	put(t, root, "/etc/.updated", "TIMESTAMP_NSEC=3000000000400\n")
	at(stamp, 3000, 0)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}})
	put(t, root, "/etc/.updated", "")
	at(stamp, 3000, 0)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}})
	// The kernel command line decides first; the initrd never updates.
	at(stamp, 9000, 0)
	put(t, root, "/proc/cmdline", "systemd.condition_needs_update=1\n")
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}})
	put(t, root, "/proc/cmdline", "systemd.condition_needs_update=0\n")
	at(stamp, 1, 0)
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, false}})
	put(t, root, "/proc/cmdline", "quiet\n")
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, true}})
	put(t, root, "/etc/initrd-release", "")
	runPred(t, PredNeedsUpdate, []predCase{{"/etc", false, false}})
	// The marker form is untouched by any of it.
	runPred(t, PredNeedsUpdate, []predCase{{"yes", false, false}, {"no", false, true}})
	if ok, why := checkNeedsUpdate("etc"); ok || !strings.Contains(why, "invalid") {
		t.Errorf("needs-update=etc = %v %q, want false + invalid", ok, why)
	}
}

// -------- ac-power ----------------------------------------------------

func supply(t *testing.T, root, name, typ, online string) {
	t.Helper()
	dir := "/sys/class/power_supply/" + name
	mkdir(t, root, dir)
	if typ != "" {
		put(t, root, dir+"/type", typ+"\n")
	}
	if online != "" {
		put(t, root, dir+"/online", online+"\n")
	}
}

func TestACPowerPredicate(t *testing.T) {
	onAC := []predCase{{"", false, true}, {"yes", false, true}, {"no", false, false}, {"yes", true, false}}
	onBat := []predCase{{"", false, false}, {"yes", false, false}, {"no", false, true}, {"no", true, false}}
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, root string)
		ac, has bool
	}{
		{"no class dir", func(*testing.T, string) {}, false, false},
		{"empty class dir", func(t *testing.T, root string) {
			mkdir(t, root, "/sys/class/power_supply")
		}, false, false},
		{"mains online", func(t *testing.T, root string) {
			supply(t, root, "AC", "Mains", "1")
			supply(t, root, "BAT0", "Battery", "")
		}, true, true},
		{"mains offline + battery", func(t *testing.T, root string) {
			supply(t, root, "AC", "Mains", "0")
			supply(t, root, "BAT0", "Battery", "")
		}, false, true},
		{"battery only", func(t *testing.T, root string) {
			supply(t, root, "BAT0", "Battery", "")
		}, false, true},
		{"one of two mains online", func(t *testing.T, root string) {
			supply(t, root, "ACA", "Mains", "0")
			supply(t, root, "ACB", "Mains", "1")
			supply(t, root, "BAT0", "Battery", "")
		}, true, true},
		{"mains offline, no battery", func(t *testing.T, root string) {
			supply(t, root, "AC", "Mains", "0")
		}, false, false},
		{"mains without online file", func(t *testing.T, root string) {
			supply(t, root, "AC", "Mains", "")
			supply(t, root, "BAT0", "Battery", "")
		}, false, true},
		{"untyped entries ignored", func(t *testing.T, root string) {
			supply(t, root, "hidpp", "", "1")
			supply(t, root, "ucsi", "USB", "1")
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withPredicateRoot(t)
			tc.setup(t, root)
			ac, has := detectACPower()
			if ac != tc.ac || has != tc.has {
				t.Fatalf("detectACPower() = (%v, %v), want (%v, %v)", ac, has, tc.ac, tc.has)
			}
			// No information at all means AC, as in systemd.
			if tc.ac || !tc.has {
				runPred(t, PredACPower, onAC)
			} else {
				runPred(t, PredACPower, onBat)
			}
		})
	}
	t.Run("invalid param", func(t *testing.T) {
		withPredicateRoot(t)
		if ok, why := checkACPower("maybe"); ok || !strings.Contains(why, "invalid") {
			t.Fatalf("ac-power=maybe = %v %q, want false + invalid", ok, why)
		}
	})
}

// -------- path-is-mount-point ---------------------------------------------

// hostMountPoints reads the mount points from /proc/self/mountinfo,
// independently of pathIsMountPoint's device comparison.
func hostMountPoints(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		t.Skipf("no mountinfo: %v", err)
	}
	defer f.Close()
	mp := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 {
			mp[fields[4]] = true
		}
	}
	return mp
}

func TestPathIsMountPointHost(t *testing.T) {
	mp := hostMountPoints(t)
	tmp := t.TempDir()
	file := filepath.Join(tmp, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, p := range []string{"/", "/proc", "/sys", "/dev", "/proc/self", "/proc/sys", "/proc/sys/kernel", "/sys/fs/cgroup", "/dev/pts", "/run", tmp, file} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		want := mp[p]
		got, why := pathIsMountPoint(p)
		if got != want {
			t.Errorf("pathIsMountPoint(%q) = %v (%q), mountinfo says %v", p, got, why, want)
		}
		checked++
	}
	if !mp["/proc"] {
		t.Skip("/proc is not a mount point here; cannot pin a positive")
	}
	runPred(t, PredPathIsMountPoint, []predCase{
		{"/proc", false, true},
		{"/proc", true, false},
		{tmp, false, false},
		{tmp, true, true},
	})
	if checked < 4 {
		t.Fatalf("only %d paths checked", checked)
	}
}

func TestPathIsMountPointMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope")
	ok, why := pathIsMountPoint(p)
	if ok || !strings.Contains(why, "nope") {
		t.Fatalf("missing path = %v %q, want false naming it", ok, why)
	}
}

// -------- group -------------------------------------------------------

func TestGroupPredicate(t *testing.T) {
	egid := os.Getegid()
	held := map[int]bool{egid: true}
	supp, _ := os.Getgroups()
	for _, g := range supp {
		held[g] = true
	}
	notHeld := 4242
	for held[notHeld] {
		notHeld++
	}
	sys := egid < 1000
	runPred(t, PredGroup, []predCase{
		{strconv.Itoa(egid), false, true},
		{"gid:" + strconv.Itoa(egid), false, true},
		{" " + strconv.Itoa(egid) + " ", false, true},
		{strconv.Itoa(egid), true, false},
		{strconv.Itoa(notHeld), false, false},
		{"gid:" + strconv.Itoa(notHeld), false, false},
		{strconv.Itoa(notHeld), true, true},
		{"@system", false, sys},
		{"@system", true, !sys},
		{"no-such-group-slinit-test", false, false},
		{"no-such-group-slinit-test", true, true},
	})
	for _, g := range supp {
		if g != egid {
			runPred(t, PredGroup, []predCase{{strconv.Itoa(g), false, true}})
			break
		}
	}
	if g, err := user.LookupGroupId(strconv.Itoa(egid)); err == nil {
		runPred(t, PredGroup, []predCase{{g.Name, false, true}, {g.Name, true, false}})
	}
	if _, why := checkGroup("no-such-group-slinit-test"); !strings.Contains(why, "lookup") {
		t.Errorf("unknown group reason %q should say lookup", why)
	}
}

// -------- os-release ------------------------------------------------------

func TestReadOSReleaseFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	content := "# comment\n" +
		"ID_LIKE=void\n" +
		"ID=sunlight\n" +
		"NAME=\"Sunlight Linux\"\n" +
		"PRETTY_NAME='Sun Light'\n" +
		"VERSION_ID=\"4.0\n" +
		"EMPTY=\n" +
		"Q=\"\n" +
		"QQ=\"\"\n" +
		"ID=second\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"ID":          "sunlight", // first match wins, ID_LIKE is not ID
		"ID_LIKE":     "void",
		"NAME":        "Sunlight Linux",
		"PRETTY_NAME": "Sun Light",
		"VERSION_ID":  "\"4.0", // unmatched quote kept
		"EMPTY":       "",
		"Q":           "\"",
		"QQ":          "",
	} {
		got, err := readOSReleaseFile(p, key)
		if err != nil || got != want {
			t.Errorf("readOSReleaseFile(%s) = %q, %v; want %q", key, got, err, want)
		}
	}
	for _, key := range []string{"VERSION", "I", "# comment"} {
		if got, err := readOSReleaseFile(p, key); err == nil {
			t.Errorf("readOSReleaseFile(%q) = %q, want an error", key, got)
		}
	}
	if _, err := readOSReleaseFile(filepath.Join(t.TempDir(), "nope"), "ID"); !os.IsNotExist(err) {
		t.Errorf("missing file err = %v, want not-exist", err)
	}
}

// -------- kernel-module-loaded --------------------------------------------

func TestKernelModuleLoadedPredicate(t *testing.T) {
	root := withPredicateRoot(t)
	// A module loaded at runtime has an initstate; a built-in one has
	// the directory (with parameters) but none. /proc/modules lists
	// only the first kind, and a built-in module was "not loaded".
	put(t, root, "/proc/modules", "nf_tables 368640 0 - Live 0x0000000000000000\n")
	put(t, root, "/sys/module/nf_tables/initstate", "live\n")
	put(t, root, "/sys/module/snd_hda_intel/initstate", "coming\n")
	put(t, root, "/sys/module/vt/parameters/default_utf8", "1\n")
	put(t, root, "/sys/module/zram/initstate", "going\n")
	put(t, root, "/sys/module/notdir", "")
	runPred(t, PredKernelModuleLoaded, []predCase{
		{"nf_tables", false, true},
		{"nf-tables", false, true},
		{" nf_tables ", false, true},
		{"vt", false, true},
		{"snd_hda_intel", false, false},
		{"zram", false, false},
		{"ext4", false, false},
		{"nf_table", false, false},
		{"NF_TABLES", false, false},
		{"notdir", false, false},
		{"../module/vt", false, false},
		{"..", false, false},
		{"nf_tables", true, false},
		{"ext4", true, true},
		{"vt", true, false},
	})
	if ok, why := checkKernelModuleLoaded("  "); ok || !strings.Contains(why, "empty") {
		t.Errorf("empty name = %v %q", ok, why)
	}
	if _, why := checkKernelModuleLoaded("snd-hda-intel"); !strings.Contains(why, "coming") {
		t.Errorf("a module still in its init should say so: %q", why)
	}
}

// -------- machine-tag -----------------------------------------------------

func TestMachineTagPredicate(t *testing.T) {
	root := withPredicateRoot(t)
	runPred(t, PredMachineTag, []predCase{{"web", false, false}, {"web", true, true}})
	if _, why := checkMachineTag("web"); !strings.Contains(why, "machine-info") {
		t.Errorf("missing file reason %q", why)
	}
	put(t, root, "/etc/machine-info", "PRETTY_HOSTNAME=\"Web 1\"\nTAGS_OLD=db\n  TAGS=\"web db-primary\"\nTAGS=late\n")
	runPred(t, PredMachineTag, []predCase{
		{"web", false, true},
		{"db-primary", false, true},
		{" web ", false, true},
		{"db", false, false},
		{"Web", false, false},
		{"late", false, false}, // first TAGS= line decides
		{"\"web", false, false},
		{"web", true, false},
		{"db", true, true},
	})
	put(t, root, "/etc/machine-info", "TAGS='edge  canary'\n")
	runPred(t, PredMachineTag, []predCase{{"edge", false, true}, {"canary", false, true}, {"'edge", false, false}})
	put(t, root, "/etc/machine-info", "TAGS=edge\n")
	runPred(t, PredMachineTag, []predCase{{"edge", false, true}})
	put(t, root, "/etc/machine-info", "TAGS=\"\"\n")
	runPred(t, PredMachineTag, []predCase{{"edge", false, false}})
	put(t, root, "/etc/machine-info", "PRETTY_HOSTNAME=x\n")
	if ok, why := checkMachineTag("edge"); ok || !strings.Contains(why, "no TAGS") {
		t.Errorf("no TAGS line = %v %q", ok, why)
	}
}

// -------- firmware --------------------------------------------------------

func TestFirmwarePredicate(t *testing.T) {
	root := withPredicateRoot(t)
	all := []string{"uefi", "bios", "device-tree", "smbios"}
	for _, k := range all {
		runPred(t, PredFirmware, []predCase{{k, false, false}, {k, true, true}})
	}
	// An unknown key with no DMI product name fails, naming the key.
	if ok, why := checkFirmware("thinkpad"); ok || !strings.Contains(why, "unknown key") {
		t.Errorf("no product_name = %v %q", ok, why)
	}
	put(t, root, "/sys/class/dmi/id/bios_vendor", "  \n")
	runPred(t, PredFirmware, []predCase{{"bios", false, false}})

	mkdir(t, root, "/sys/firmware/efi")
	runPred(t, PredFirmware, []predCase{{"uefi", false, true}, {" UEFI ", false, true}, {"uefi", true, false}, {"device-tree", false, false}})
	mkdir(t, root, "/sys/firmware/devicetree")
	runPred(t, PredFirmware, []predCase{{"device-tree", false, true}, {"smbios", false, false}})
	mkdir(t, root, "/sys/firmware/dmi/tables")
	runPred(t, PredFirmware, []predCase{{"smbios", false, true}, {"bios", false, false}})
	put(t, root, "/sys/class/dmi/id/bios_vendor", "SeaBIOS\n")
	runPred(t, PredFirmware, []predCase{{"bios", false, true}, {"bios", true, false}})

	put(t, root, "/sys/class/dmi/id/product_name", "ThinkPad X1 Carbon Gen 9\n")
	runPred(t, PredFirmware, []predCase{
		{"thinkpad", false, true},
		{"X1 Carbon", false, true},
		{"dell", false, false},
		{"dell", true, true},
	})
}

// -------- file-value ------------------------------------------------------

func TestFileValuePredicate(t *testing.T) {
	dir := t.TempDir()
	w := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	typ := w("type", "0\n")
	crlf := w("crlf", "on \t\r\n")
	empty := w("empty", "")
	colon := w("a:b", "x\n")
	lead := w("lead", " 1\n")
	big := w("big", strings.Repeat("a", 4096)+"tail")
	runPred(t, PredFileValue, []predCase{
		{typ + ":0", false, true},
		{typ + ":1", false, false},
		{typ + ":0", true, false},
		{typ + ":1", true, true},
		{typ + ":0\n", false, false},
		{crlf + ":on", false, true},
		{empty + ":", false, true},
		{empty + ":0", false, false},
		{colon + ":x", false, true}, // the last ':' separates
		{lead + ":1", false, false}, // leading space is content
		{lead + ": 1", false, true},
		{big + ":" + strings.Repeat("a", 4096), false, true}, // read capped at 4KB
		{filepath.Join(dir, "nope") + ":0", false, false},
		{typ, false, false},
		{":0", false, false},
	})
	for param, frag := range map[string]string{
		typ:                               "missing ':'",
		":0":                              "empty path",
		filepath.Join(dir, "nope") + ":0": "no such file",
		typ + ":1":                        `reads "0", want "1"`,
	} {
		if _, why := checkFileValue(param); !strings.Contains(why, frag) {
			t.Errorf("checkFileValue(%q) reason %q lacks %q", param, why, frag)
		}
	}
}

// -------- String / PredicateKindByName / evalRaw --------------------------

func TestPredicateNamesRoundTrip(t *testing.T) {
	seen := map[string]PredicateKind{}
	for k := PredPathExists; k <= PredServiceStarted; k++ {
		if k == PredExecCondition {
			continue
		}
		s := Predicate{Kind: k, Param: "p"}.String()
		name, ok := strings.CutPrefix(s, "condition-")
		if !ok || !strings.HasSuffix(name, "=p") {
			t.Errorf("kind %d: String() = %q", k, s)
			continue
		}
		name = strings.TrimSuffix(name, "=p")
		if prev, dup := seen[name]; dup {
			t.Errorf("kinds %d and %d both render as %q", prev, k, name)
		}
		seen[name] = k
		if got, ok := PredicateKindByName(name); !ok || got != k {
			t.Errorf("PredicateKindByName(%q) = %d, %v; want %d", name, got, ok, k)
		}
		if a := (Predicate{Kind: k, Param: "p", IsAssert: true, Negate: true}).String(); a != "assert-"+name+"=!p" {
			t.Errorf("kind %d assert+negate String() = %q", k, a)
		}
	}
	if _, ok := PredicateKindByName("exec-condition"); ok {
		t.Error("exec-condition is its own directive, not a condition-* name")
	}
	for _, bad := range []string{"", "Path-Exists", "path_exists", "condition-path-exists", "kind-3"} {
		if _, ok := PredicateKindByName(bad); ok {
			t.Errorf("PredicateKindByName(%q) accepted", bad)
		}
	}
}

func TestPredicateStringExecAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		p    Predicate
		want string
	}{
		{Predicate{Kind: PredExecCondition, Param: "test -f /x"}, "exec-condition=test -f /x"},
		{Predicate{Kind: PredExecCondition, Param: "true", Privileged: true}, "exec-condition=+true"},
		{Predicate{Kind: PredExecCondition, Param: "true", IsAssert: true}, "assert-exec-condition=true"},
		{Predicate{Kind: PredExecCondition, Param: "true", IsAssert: true, Privileged: true}, "assert-exec-condition=+true"},
		{Predicate{Kind: 200, Param: "x"}, "condition-kind-200=x"},
		{Predicate{Kind: 200, Param: "x", Negate: true, IsAssert: true}, "assert-kind-200=!x"},
	} {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestEvalRawUnknownAndServiceStarted(t *testing.T) {
	if ok, why := (Predicate{Kind: 200}).Evaluate(); ok || !strings.Contains(why, "unknown predicate kind 200") {
		t.Errorf("unknown kind = %v %q", ok, why)
	}
	// Negating an unknown kind must not turn it into a pass reason-free
	// by accident of message: the raw answer is false, so !kind is true.
	if ok, _ := (Predicate{Kind: 200, Negate: true}).Evaluate(); !ok {
		t.Error("negated unknown kind should be true (raw false)")
	}
	if ok, why := (Predicate{Kind: PredServiceStarted, Param: "x"}).Evaluate(); ok || !strings.Contains(why, "service set") {
		t.Errorf("service-started via Evaluate = %v %q", ok, why)
	}
}

// TestEvalRawDispatch pins which check each kind reaches: every param
// below fails, and only the intended check words its reason this way.
func TestEvalRawDispatch(t *testing.T) {
	withPredicateRoot(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, tc := range []struct {
		kind  PredicateKind
		param string
		frag  string
	}{
		{PredPathExists, missing, "does not exist"},
		{PredPathExistsGlob, "[", "invalid glob"},
		{PredPathIsDirectory, missing, "is not a directory"},
		{PredPathIsMountPoint, missing, "path \""},
		{PredFileNotEmpty, missing, "file \""},
		{PredDirectoryNotEmpty, missing, "dir \""},
		{PredKernelCommandLine, "quiet", "/proc/cmdline"},
		{PredVirtualization, "bogus-virt", "expected \"bogus-virt\""},
		{PredFirstBoot, "bogus", "invalid first-boot"},
		{PredHost, "no-such-host.invalid", "host \""},
		{PredSecurity, "bogus", "unknown security framework"},
		{PredNeedsUpdate, "bogus", "invalid needs-update"},
		{PredACPower, "bogus", "invalid ac-power"},
		{PredNetworkOnline, "bogus", "invalid network-online"},
		{PredPathIsSocket, missing, "path \""},
		{PredFraction, "x", "fraction:"},
		{PredArchitecture, "bogus", "architecture:"},
		{PredCPUFeature, "", "cpu-feature:"},
		{PredCPUs, "x", "cpus:"},
		{PredMemory, "x", "memory:"},
		{PredKernelVersion, "<0", "kernel-version:"},
		{PredKernelModuleLoaded, "ext4", "kernel-module-loaded:"},
		{PredOSRelease, "x", "os-release:"},
		{PredUser, "no-such-user-slinit-test", "user:"},
		{PredGroup, "no-such-group-slinit-test", "group:"},
		{PredEnvironment, "=", "environment:"},
		{PredFileIsExecutable, "", "file-is-executable:"},
		{PredPathIsSymbolicLink, "", "path-is-symbolic-link:"},
		{PredPathIsReadWrite, "", "path-is-read-write:"},
		{PredFirmware, "uefi", "/sys/firmware/efi"},
		{PredMachineTag, "web", "machine-tag:"},
		{PredCredential, "", "credential:"},
		{PredControlGroupController, "", "control-group-controller:"},
		{PredMemoryPressure, "<0", "/proc/pressure/memory"},
		{PredCPUPressure, "<0", "/proc/pressure/cpu"},
		{PredIOPressure, "<0", "/proc/pressure/io"},
		{PredExecCondition, " ", "exec-condition: empty"},
		{PredBootCond, "", "boot-cond"},
		{PredFileValue, "nocolon", "file-value:"},
	} {
		ok, why := Predicate{Kind: tc.kind, Param: tc.param}.Evaluate()
		if ok || !strings.Contains(why, tc.frag) {
			t.Errorf("%s = %v %q, want false with %q",
				Predicate{Kind: tc.kind, Param: tc.param}.String(), ok, why, tc.frag)
		}
	}
}

// TestPathIsMountPointSpelling: the answer is about the directory the
// path names, as systemd's path_is_mount_point(AT_SYMLINK_FOLLOW) has
// it, not about how the path is spelled. A trailing slash or "." made
// the parent the directory itself, and a symlink made the parent the
// link's own directory, so /proc/ read as no mount point and a link to
// /proc/sys/kernel as one.
func TestPathIsMountPointSpelling(t *testing.T) {
	mp := hostMountPoints(t)
	if !mp["/proc"] || mp["/proc/sys/kernel"] {
		t.Skip("needs /proc mounted and /proc/sys/kernel not a mount point")
	}
	dir := t.TempDir()
	toProc := filepath.Join(dir, "to-proc")
	toKernel := filepath.Join(dir, "to-kernel")
	if err := os.Symlink("/proc", toProc); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/sys/kernel", toKernel); err != nil {
		t.Fatal(err)
	}
	cases := []predCase{
		{"/proc/", false, true},
		{"/proc/.", false, true},
		{"/proc//", false, true},
		{"/proc/sys/kernel/", false, false},
		{"//", false, true},
		{toProc, false, true},
		{toKernel, false, false},
		{toKernel, true, true},
	}
	// In a container /proc/sys is a read-only bind of the same procfs:
	// mountinfo lists it, and the kernel's mount-root attribute sees
	// it, where a device comparison could not.
	cases = append(cases, predCase{"/proc/sys/kernel/..", false, mp["/proc/sys"]})
	runPred(t, PredPathIsMountPoint, cases)
}
