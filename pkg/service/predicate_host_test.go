package service

import (
	"bufio"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The tests in this file point predicateRoot at a fixture tree, so the
// machine predicates read /proc, /sys, /etc and /run files the test
// wrote instead of the host's. None of them may run in parallel.

// withPredicateRoot points the fixed host paths at a fresh fixture
// root for the length of the test and returns that root.
func withPredicateRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	orig := predicateRoot
	predicateRoot = root
	t.Cleanup(func() { predicateRoot = orig })
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

func TestVirtualizationDetect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		files map[string]string
		want  string
	}{
		{"bare metal", "", map[string]string{
			"/proc/cpuinfo":              "flags\t\t: fpu vme sse2\n",
			"/proc/sys/kernel/osrelease": "6.12.0-sunlight\n",
			"/proc/1/cgroup":             "0::/init.scope\n",
		}, ""},
		{"nothing readable", "", nil, ""},
		{"container env", "systemd-nspawn", nil, "systemd-nspawn"},
		{"env beats cgroup", "lxc", map[string]string{"/proc/1/cgroup": "0::/docker/abc\n"}, "lxc"},
		{"docker cgroup", "", map[string]string{"/proc/1/cgroup": "0::/docker/abc\n"}, "docker"},
		{"lxc cgroup", "", map[string]string{"/proc/1/cgroup": "0::/lxc/c1\n"}, "lxc"},
		{"kubepods cgroup", "", map[string]string{"/proc/1/cgroup": "0::/kubepods/burstable/p1\n"}, "kubernetes"},
		{"podman cgroup", "", map[string]string{"/proc/1/cgroup": "0::/machine.slice/libpod-podman-1\n"}, "podman"},
		{"container beats hypervisor", "", map[string]string{
			"/proc/1/cgroup": "0::/docker/abc\n",
			"/proc/cpuinfo":  "flags\t\t: fpu hypervisor\n",
		}, "docker"},
		{"hypervisor no dmi", "", map[string]string{"/proc/cpuinfo": "flags\t\t: fpu hypervisor\n"}, "vm"},
		{"qemu vendor", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/sys_vendor": "QEMU\n",
		}, "qemu"},
		{"qemu product", "", map[string]string{
			"/proc/cpuinfo":                  "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/product_name": "Standard PC (Q35 + ICH9, 2009) QEMU\n",
		}, "qemu"},
		{"vmware", "", map[string]string{
			"/proc/cpuinfo":                  "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/product_name": "VMware Virtual Platform\n",
		}, "vmware"},
		{"virtualbox", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/sys_vendor": "innotek GmbH\n",
		}, "virtualbox"},
		{"hyper-v", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/sys_vendor": "Microsoft Corporation\n",
		}, "microsoft"},
		{"xen", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/sys_vendor": "Xen\n",
		}, "xen"},
		{"gce", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/sys_vendor": "Google\n",
		}, "google"},
		{"unknown dmi", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: hypervisor\n",
			"/sys/class/dmi/id/sys_vendor": "Acme\n",
		}, "vm"},
		// DMI is only consulted when the CPU says it runs under a
		// hypervisor: a QEMU vendor string alone is not a VM.
		{"dmi without hypervisor", "", map[string]string{
			"/proc/cpuinfo":                "flags\t\t: fpu\n",
			"/sys/class/dmi/id/sys_vendor": "QEMU\n",
		}, ""},
		{"wsl", "", map[string]string{
			"/proc/cpuinfo":              "flags\t\t: fpu\n",
			"/proc/sys/kernel/osrelease": "5.15.90.1-Microsoft-standard-WSL2\n",
		}, "wsl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withPredicateRoot(t)
			t.Setenv("container", tc.env)
			for p, c := range tc.files {
				put(t, root, p, c)
			}
			if got := detectVirtualization(); got != tc.want {
				t.Fatalf("detectVirtualization() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVirtualizationPredicate(t *testing.T) {
	t.Run("bare metal", func(t *testing.T) {
		withPredicateRoot(t)
		t.Setenv("container", "")
		runPred(t, PredVirtualization, []predCase{
			{"", false, false},
			{"yes", false, false},
			{"no", false, true},
			{"kvm", false, false},
			{"no", true, false},
			{"yes", true, true},
		})
		if _, why := checkVirtualization("kvm"); !strings.Contains(why, "no virt") {
			t.Errorf("reason %q should say no virtualization was detected", why)
		}
	})
	t.Run("qemu", func(t *testing.T) {
		root := withPredicateRoot(t)
		t.Setenv("container", "")
		put(t, root, "/proc/cpuinfo", "flags\t\t: hypervisor\n")
		put(t, root, "/sys/class/dmi/id/sys_vendor", "QEMU\n")
		runPred(t, PredVirtualization, []predCase{
			{"", false, true},
			{"yes", false, true},
			{"no", false, false},
			{"qemu", false, true},
			{"kvm", false, false},
			{"QEMU", false, false},
			{"qemu", true, false},
			{"no", true, true},
		})
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
		runPred(t, PredSecurity, []predCase{{"audit", false, false}})
		put(t, root, "/sys/kernel/security/lsm", "capability,landlock,yama,apparmor\n")
		runPred(t, PredSecurity, []predCase{{"audit", false, false}})
		put(t, root, "/sys/kernel/security/lsm", "capability,audit\n")
		runPred(t, PredSecurity, []predCase{{"audit", false, true}, {"audit", true, false}})
	})
	t.Run("measured-os", func(t *testing.T) {
		root := withPredicateRoot(t)
		const log = "/sys/kernel/security/tpm0/binary_bios_measurements"
		runPred(t, PredSecurity, []predCase{{"measured-os", false, false}})
		put(t, root, log, "")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, false}})
		put(t, root, log, "\x01\x02")
		runPred(t, PredSecurity, []predCase{{"measured-os", false, true}, {"measured-os", true, false}})
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
	// systemd's own spelling takes a directory; slinit rejects it.
	if ok, why := checkNeedsUpdate("/etc"); ok || !strings.Contains(why, "invalid") {
		t.Errorf("needs-update=/etc = %v %q, want false + invalid", ok, why)
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
	for _, p := range []string{"/", "/proc", "/sys", "/dev", "/proc/self", "/proc/sys/kernel", tmp, file} {
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
	put(t, root, "/proc/modules",
		"nf_tables 368640 0 - Live 0x0000000000000000\n"+
			"ext4 1048576 1 - Live 0x0000000000000000\n"+
			"\n"+
			"snd_hda_intel 61440 2 - Loading 0x0000000000000000\n")
	runPred(t, PredKernelModuleLoaded, []predCase{
		{"ext4", false, true},
		{" ext4 ", false, true},
		{"nf_tables", false, true},
		{"snd_hda_intel", false, true},
		{"ext", false, false},
		{"EXT4", false, false},
		{"368640", false, false},
		{"Live", false, false},
		{"vfat", false, false},
		{"ext4", true, false},
		{"vfat", true, true},
	})
	if ok, why := checkKernelModuleLoaded("  "); ok || !strings.Contains(why, "empty") {
		t.Errorf("empty name = %v %q", ok, why)
	}
	if err := os.Remove(filepath.Join(root, "/proc/modules")); err != nil {
		t.Fatal(err)
	}
	if ok, why := checkKernelModuleLoaded("ext4"); ok || !strings.Contains(why, "/proc/modules") {
		t.Errorf("missing /proc/modules = %v %q", ok, why)
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
	runPred(t, PredPathIsMountPoint, []predCase{
		{"/proc/", false, true},
		{"/proc/.", false, true},
		{"/proc//", false, true},
		{"/proc/sys/kernel/", false, false},
		{"/proc/sys/kernel/..", false, mp["/proc/sys"]},
		{"//", false, true},
		{toProc, false, true},
		{toKernel, false, false},
		{toKernel, true, true},
	})
}
