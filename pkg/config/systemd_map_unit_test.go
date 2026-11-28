package config

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// convertUnitSection converts a unit whose [Unit] section holds lines,
// and returns the emitted file and the conversion's warnings.
func convertUnitSection(t *testing.T, lines string) (string, []SystemdWarning, error) {
	t.Helper()
	p := writeUnit(t, t.TempDir(), "probe.service",
		"[Unit]\n"+lines+"\n[Service]\nExecStart=/bin/true\n")
	cfg, warns, err := ConvertSystemdUnit(p)
	if err != nil {
		return "", warns, err
	}
	var buf bytes.Buffer
	EmitSlinitFile(&buf, cfg)
	return buf.String(), warns, nil
}

// emittedLines are the lines of out that start with one of prefixes.
func emittedLines(out string, prefixes ...string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		for _, p := range prefixes {
			if strings.HasPrefix(l, p) {
				got = append(got, l)
				break
			}
		}
	}
	return got
}

// warnWith reports whether a warning of level mentions sub.
func warnWith(warns []SystemdWarning, level, sub string) bool {
	for _, w := range warns {
		if w.Level == level && strings.Contains(w.Msg, sub) {
			return true
		}
	}
	return false
}

type sdUnitCase struct {
	name  string
	lines string
	want  []string // the emitted condition-/assert- lines, in order
	warn  string   // "LEVEL:substring" expected among the warnings
	err   string   // substring of the refusal, when the unit is refused
}

func runSDUnitCases(t *testing.T, cases []sdUnitCase, prefixes ...string) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, warns, err := convertUnitSection(t, tc.lines)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one naming %q\n%s", err, tc.err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			got := emittedLines(out, prefixes...)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("emitted %q, want %q\nwarnings: %v", got, tc.want, warns)
			}
			if tc.warn != "" {
				level, sub, _ := strings.Cut(tc.warn, ":")
				if !warnWith(warns, level, sub) {
					t.Errorf("no %s naming %q in %v", level, sub, warns)
				}
			} else {
				for _, w := range warns {
					if w.Level == "WARN" {
						t.Errorf("unexpected WARN: %s", w.Msg)
					}
				}
			}
		})
	}
}

func TestSystemdConditionPrefixesAndReset(t *testing.T) {
	runSDUnitCases(t, []sdUnitCase{
		{name: "plain", lines: "ConditionPathExists=/etc/hostname",
			want: []string{"condition-path-exists = /etc/hostname"}},
		{name: "negated", lines: "ConditionPathExists=!/run/x",
			want: []string{"condition-path-exists = !/run/x"}},
		{name: "assertion", lines: "AssertPathIsDirectory=/var",
			want: []string{"assert-path-is-directory = /var"}},
		{name: "string form skips blanks after !", lines: "ConditionHost=! box",
			want: []string{"condition-host = !box"}},
		// systemd ignores a relative path when it loads the unit, for an
		// assertion too, so dropping it is what systemd does.
		{name: "relative path", lines: "ConditionPathExists=etc/x",
			warn: "WARN:must be absolute"},
		{name: "relative path, assertion", lines: "AssertPathExists=etc/x",
			warn: "WARN:must be absolute"},
		{name: "path form keeps the blank after !", lines: "ConditionPathExists=! /x",
			warn: "WARN:must be absolute"},
		// The only triggering check of its list must hold, as a plain
		// one must; two or more need one of them, which slinit cannot
		// express.
		{name: "lone triggering condition", lines: "ConditionPathExists=|/a",
			want: []string{"condition-path-exists = /a"}},
		{name: "lone triggering condition, negated, beside plain ones",
			lines: "ConditionPathExists=/b\nConditionHost=|! box\nAssertPathExists=|/c",
			want:  []string{"condition-path-exists = /b", "condition-host = !box", "assert-path-exists = /c"}},
		// Since 4.1 slinit's "|" means what systemd's does.
		{name: "triggering conditions", lines: "ConditionPathExists=|/a\nConditionPathExists=|/b",
			want: []string{"condition-path-exists = |/a", "condition-path-exists = |/b"}},
		{name: "triggering assertions", lines: "AssertPathExists=|/a\nAssertHost=|box",
			want: []string{"assert-path-exists = |/a", "assert-host = |box"}},
		// A triggering check slinit cannot read still counts: without
		// it, "one of them" would be /a alone, stricter than systemd.
		{name: "triggering, one unexpandable", lines: "ConditionHost=|%H\nConditionPathExists=|/a",
			want: []string{}, warn: "WARN:cannot be checked"},
		// One condition and one assertion are each alone in their list.
		{name: "triggering in each list", lines: "ConditionPathExists=|/a\nAssertPathExists=|/b",
			want: []string{"condition-path-exists = /a", "assert-path-exists = /b"}},
		// An empty Condition*= empties the triggering ones as well.
		{name: "reset takes back a triggering condition",
			lines: "ConditionPathExists=|/a\nConditionHost=\nConditionPathExists=|/b",
			want:  []string{"condition-path-exists = /b"}},
		{name: "a gap is unchecked, triggering or not", lines: "ConditionPathIsEncrypted=|/x",
			want: []string{}, warn: "WARN:encrypted"},
		{name: "repeated assignments add up", lines: "ConditionPathExists=/a\nConditionPathExists=/b\nConditionPathIsDirectory=/c",
			want: []string{"condition-path-exists = /a", "condition-path-exists = /b", "condition-path-is-directory = /c"}},
		{name: "an empty condition empties every condition",
			lines: "ConditionPathExists=/a\nConditionHost=box\nConditionKernelCommandLine=\nConditionPathExists=/b",
			want:  []string{"condition-path-exists = /b"}},
		{name: "an empty condition leaves the assertions",
			lines: "AssertPathExists=/a\nConditionPathExists=/b\nConditionUser=",
			want:  []string{"assert-path-exists = /a"}},
		{name: "an empty assertion leaves the conditions and Requisite=",
			lines: "Requisite=dbus.service\nAssertPathExists=/a\nConditionPathExists=/b\nAssertHost=",
			want:  []string{"assert-service-started = dbus", "condition-path-exists = /b"},
			warn:  "NOTE:Requisite"},
		{name: "an empty assertion takes back an unchecked one",
			lines: "AssertCapability=CAP_SYS_ADMIN\nAssertPathExists=\nAssertPathExists=/a",
			want:  []string{"assert-path-exists = /a"}},
		{name: "dollar is literal", lines: "ConditionEnvironment=FOO=$bar",
			want: []string{"condition-environment = FOO=$$bar"}, warn: "NOTE:setenv"},
		{name: "specifier expanded", lines: "AssertPathExists=/run/%N.pid",
			want: []string{"assert-path-exists = /run/probe.pid"}},
		{name: "specifier slinit cannot expand", lines: "ConditionHost=%H", warn: "WARN:specifier"},
		{name: "specifier slinit cannot expand, assertion", lines: "AssertHost=%H", err: "specifier"},
		{name: "nothing after the prefix", lines: "ConditionHost=!", warn: "WARN:nothing follows"},
	}, "condition-", "assert-")
}

// A drop-in's triggering check joins the unit's: the unit's lone one
// is no longer alone. And one only in the drop-in is.
func TestSystemdTriggeringDropin(t *testing.T) {
	for _, c := range []struct {
		name, unit, dropin string
		want               []string
	}{
		{"two together", "ConditionPathExists=|/a\n", "ConditionPathExists=|/b\n",
			[]string{"condition-path-exists = |/a", "condition-path-exists = |/b"}},
		{"one in the drop-in", "ConditionPathExists=/a\n", "ConditionPathExists=|/b\n",
			[]string{"condition-path-exists = /a", "condition-path-exists = /b"}},
		{"drop-in resets the unit's", "ConditionPathExists=|/a\n", "ConditionPathExists=\nConditionPathExists=|/b\n",
			[]string{"condition-path-exists = /b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			p := writeUnit(t, dir, "probe.service", "[Unit]\n"+c.unit+"[Service]\nExecStart=/bin/true\n")
			writeTree(t, filepath.Join(dir, "probe.service.d", "10-x.conf"), "[Unit]\n"+c.dropin)
			cfg, warns, err := ConvertSystemdUnitIn(p, []string{dir})
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			EmitSlinitFile(&buf, cfg)
			got := emittedLines(buf.String(), "condition-")
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("emitted %q, want %q\nwarnings: %v", got, c.want, warns)
			}

		})
	}
}

func TestSystemdConditionKinds(t *testing.T) {
	cases := []sdUnitCase{
		// Virtualization: booleans and the ids slinit reports as systemd does.
		{name: "virt bool", lines: "ConditionVirtualization=true", want: []string{"condition-virtualization = yes"}},
		{name: "virt no", lines: "ConditionVirtualization=0", want: []string{"condition-virtualization = no"}},
		{name: "virt id", lines: "ConditionVirtualization=!vmware", want: []string{"condition-virtualization = !vmware"}},
		{name: "virt category", lines: "ConditionVirtualization=!container", want: []string{"condition-virtualization = !container"}},
		{name: "virt kvm", lines: "ConditionVirtualization=kvm", want: []string{"condition-virtualization = kvm"}},
		{name: "virt assert category", lines: "AssertVirtualization=vm", want: []string{"assert-virtualization = vm"}},
		{name: "virt private users", lines: "ConditionVirtualization=private-users", want: []string{"condition-virtualization = private-users"}},
		{name: "virt container-other", lines: "ConditionVirtualization=container-other", want: []string{"condition-virtualization = container-other"}},
		{name: "virt openvz", lines: "ConditionVirtualization=openvz", want: []string{"condition-virtualization = openvz"}},
		// slinit's "qemu" also takes a KVM guest with QEMU's DMI.
		{name: "virt qemu", lines: "ConditionVirtualization=qemu", warn: "WARN:KVM"},
		{name: "virt qemu, assertion", lines: "AssertVirtualization=qemu", err: "KVM"},
		{name: "virt slinit id", lines: "ConditionVirtualization=kubernetes", warn: "WARN:cannot tell"},
		{name: "virt none", lines: "ConditionVirtualization=none", warn: "WARN:cannot tell"},

		// CPUs / Memory: systemd's implied operator is >=, slinit's ==.
		{name: "cpus bare", lines: "ConditionCPUs=4", want: []string{"condition-cpus = >=4"}},
		{name: "cpus op", lines: "ConditionCPUs=>2", want: []string{"condition-cpus = >2"}},
		{name: "cpus unequal", lines: "ConditionCPUs=<>4", want: []string{"condition-cpus = !==4"}},
		{name: "cpus negated equal", lines: "ConditionCPUs=!=4", want: []string{"condition-cpus = !==4"}},
		{name: "cpus junk", lines: "ConditionCPUs=lots", warn: "WARN:number of CPUs"},
		{name: "memory suffix", lines: "ConditionMemory=1G", want: []string{"condition-memory = >=1073741824"}},
		{name: "memory fraction", lines: "ConditionMemory=1.5G", want: []string{"condition-memory = >=1610612736"}},
		{name: "memory op", lines: "AssertMemory=<= 512M", want: []string{"assert-memory = <=536870912"}},
		{name: "memory junk", lines: "ConditionMemory=1GB", warn: "WARN:not a size"},

		// Versions.
		{name: "kver ge", lines: "ConditionKernelVersion=>=6.1", want: []string{"condition-kernel-version = >=6.1"}},
		{name: "kver blank after op", lines: "ConditionKernelVersion=>= 5.4", want: []string{"condition-kernel-version = >=5.4"}},
		{name: "kver range", lines: "ConditionKernelVersion=>=5.4 <7",
			want: []string{"condition-kernel-version = >=5.4", "condition-kernel-version = <7"}},
		{name: "kver negated range", lines: "ConditionKernelVersion=!>=5.4 <7", warn: "WARN:negate"},
		{name: "kver glob", lines: "ConditionKernelVersion=6.1.*", warn: "WARN:glob"},
		{name: "kver gt", lines: "ConditionKernelVersion=>6.1", warn: "WARN:only with >= and <"},
		{name: "kver string equal", lines: "ConditionKernelVersion==6.1.0", warn: "WARN:only with >= and <"},
		{name: "kver suffix", lines: "ConditionKernelVersion=>=6.1-rc1", warn: "WARN:up to three numbers"},
		{name: "version kernel word", lines: "ConditionVersion=kernel >= 5.4", want: []string{"condition-kernel-version = >=5.4"}},
		{name: "version bare", lines: "AssertVersion=<7", want: []string{"assert-kernel-version = <7"}},
		{name: "version systemd", lines: "ConditionVersion=systemd >= 250", warn: "WARN:no systemd version"},
		{name: "version glibc", lines: "AssertVersion=glibc>=2.34", err: "no glibc version"},

		// Architecture.
		{name: "arch x86-64", lines: "ConditionArchitecture=x86-64", want: []string{"condition-architecture = x86_64"}},
		{name: "arch arm64", lines: "ConditionArchitecture=!arm64", want: []string{"condition-architecture = !arm64"}},
		{name: "arch native", lines: "ConditionArchitecture=native"},
		{name: "arch not native", lines: "ConditionArchitecture=!native", warn: "WARN:never holds"},
		{name: "arch go spelling", lines: "ConditionArchitecture=amd64", warn: "WARN:not an architecture"},

		// CPU features: prefixed forms do not depend on the host.
		{name: "cpufeature prefixed", lines: "ConditionCPUFeature=x86-64.fma3", want: []string{"condition-cpu-feature = x86_64.fma"}},
		{name: "cpufeature arm", lines: "ConditionCPUFeature=arm64.bti", want: []string{"condition-cpu-feature = arm64.bti"}},
		{name: "cpufeature arm upper", lines: "ConditionCPUFeature=!arm64.SVE2", want: []string{"condition-cpu-feature = !arm64.sve2"}},
		{name: "cpufeature arm unknown", lines: "ConditionCPUFeature=arm64.avx2", warn: "WARN:does not know"},
		{name: "cpufeature riscv", lines: "ConditionCPUFeature=riscv64.v", warn: "WARN:x86 and arm64"},
		{name: "cpufeature unknown", lines: "ConditionCPUFeature=x86.avx512f", warn: "WARN:does not know"},

		// os-release.
		{name: "osrelease", lines: "ConditionOSRelease=ID=debian", want: []string{"condition-os-release = ID=debian"}},
		{name: "osrelease list", lines: "ConditionOSRelease=ID=debian VERSION_ID!=12",
			want: []string{"condition-os-release = ID=debian", "condition-os-release = !VERSION_ID=12"}},
		{name: "osrelease negated", lines: "AssertOSRelease=!ID=fedora", want: []string{"assert-os-release = !ID=fedora"}},
		{name: "osrelease version op", lines: "ConditionOSRelease=VERSION_ID>=12", warn: "WARN:only with = and !="},
		{name: "osrelease glob", lines: "ConditionOSRelease=ID$=deb*", warn: "WARN:only with = and !="},

		// Host, machine tag, fraction, firmware, security.
		{name: "host", lines: "ConditionHost=box", want: []string{"condition-host = box"}},
		{name: "host glob", lines: "ConditionHost=web*", warn: "WARN:wildcards"},
		{name: "host machine id", lines: "ConditionHost=0123456789abcdef0123456789abcdef", warn: "WARN:machine ID"},
		{name: "machine tag", lines: "ConditionMachineTag=prod", want: []string{"condition-machine-tag = prod"}},
		{name: "machine tag glob", lines: "ConditionMachineTag=prod.*", warn: "WARN:wildcards"},
		{name: "fraction", lines: "ConditionFraction=50%", want: []string{"condition-fraction = :50"}, warn: "NOTE:hash"},
		{name: "fraction tag", lines: "ConditionFraction=rollout 12.5%", want: []string{"condition-fraction = rollout:12.5"}, warn: "NOTE:hash"},
		{name: "fraction permille", lines: "ConditionFraction=5‰", want: []string{"condition-fraction = :0.5"}, warn: "NOTE:hash"},
		{name: "fraction bare number", lines: "ConditionFraction=50", warn: "WARN:not a percentage"},
		{name: "firmware uefi", lines: "ConditionFirmware=uefi", want: []string{"condition-firmware = uefi"}},
		{name: "firmware smbios", lines: "ConditionFirmware=smbios-field(board_name = Foo)", warn: "WARN:firmware"},
		{name: "security", lines: "ConditionSecurity=!selinux", want: []string{"condition-security = !selinux"}},
		{name: "security audit", lines: "ConditionSecurity=audit", want: []string{"condition-security = audit"}, warn: "NOTE:libaudit"},
		{name: "security measured-os", lines: "AssertSecurity=measured-os", want: []string{"assert-security = measured-os"}},
		{name: "security tpm2", lines: "AssertSecurity=tpm2", err: "tpm2"},

		// Users, groups, booleans, environment, credentials.
		{name: "user system", lines: "ConditionUser=@system", want: []string{"condition-user = @system"}},
		{name: "user uid prefix", lines: "ConditionUser=uid:0", warn: "WARN:user name to systemd"},
		{name: "group", lines: "ConditionGroup=wheel", want: []string{"condition-group = wheel"}},
		{name: "group system", lines: "ConditionGroup=@system", warn: "WARN:group name to systemd"},
		{name: "first boot", lines: "ConditionFirstBoot=true", want: []string{"condition-first-boot = yes"}},
		{name: "ac power", lines: "AssertACPower=off", want: []string{"assert-ac-power = no"}},
		{name: "first boot junk", lines: "ConditionFirstBoot=maybe", warn: "WARN:not a boolean"},
		{name: "credential", lines: "ConditionCredential=tls.key", want: []string{"condition-credential = tls.key"}},

		// cgroup controllers.
		{name: "cgroup one", lines: "ConditionControlGroupController=memory", want: []string{"condition-control-group-controller = memory"}},
		{name: "cgroup list", lines: "ConditionControlGroupController=cpu memory",
			want: []string{"condition-control-group-controller = cpu", "condition-control-group-controller = memory"}},
		{name: "cgroup v2", lines: "ConditionControlGroupController=v2"},
		{name: "cgroup not v1", lines: "ConditionControlGroupController=!v1"},
		{name: "cgroup v1", lines: "ConditionControlGroupController=v1", warn: "WARN:never holds"},
		{name: "cgroup bpf", lines: "ConditionControlGroupController=bpf-firewall", warn: "WARN:bpf-firewall"},
		{name: "cgroup unknown ignored", lines: "ConditionControlGroupController=hugetlb memory",
			want: []string{"condition-control-group-controller = memory"}},

		// Paths with a twist.
		{name: "glob", lines: "ConditionPathExistsGlob=/dev/tpm*", want: []string{"condition-path-exists-glob = /dev/tpm*"}},
		{name: "glob leading wildcard", lines: "ConditionPathExistsGlob=/etc/foo.d/*.conf",
			want: []string{"condition-path-exists-glob = /etc/foo.d/*.conf"}, warn: "NOTE:dot"},
		{name: "glob brace", lines: "ConditionPathExistsGlob=/dev/{a,b}", warn: "WARN:{a,b}"},
		{name: "dir not empty", lines: "ConditionDirectoryNotEmpty=/etc/foo.d", want: []string{"condition-directory-not-empty = /etc/foo.d"}},
		{name: "dir not empty, assertion", lines: "AssertDirectoryNotEmpty=!/etc/foo.d", want: []string{"assert-directory-not-empty = !/etc/foo.d"}},
		{name: "mount point", lines: "ConditionPathIsMountPoint=/sys/fs/bpf", want: []string{"condition-path-is-mount-point = /sys/fs/bpf"}},
		{name: "symlink", lines: "ConditionPathIsSymbolicLink=/etc/localtime", want: []string{"condition-path-is-symbolic-link = /etc/localtime"}},
		{name: "read-write", lines: "AssertPathIsReadWrite=/var", want: []string{"assert-path-is-read-write = /var"}},
		{name: "socket", lines: "ConditionPathIsSocket=/run/dbus/system_bus_socket", want: []string{"condition-path-is-socket = /run/dbus/system_bus_socket"}},
		{name: "executable", lines: "ConditionFileIsExecutable=/usr/bin/foo", want: []string{"condition-file-is-executable = /usr/bin/foo"}},

		// Kinds slinit has no evaluator for, and three it gained in 4.1.
		{name: "capability", lines: "ConditionCapability=CAP_SYS_ADMIN", want: []string{"condition-capability = CAP_SYS_ADMIN"}},
		{name: "capability negated", lines: "ConditionCapability=!cap_net_admin", want: []string{"condition-capability = !CAP_NET_ADMIN"}},
		{name: "capability assert", lines: "AssertCapability=CAP_SYS_ADMIN", want: []string{"assert-capability = CAP_SYS_ADMIN"}},
		{name: "capability unknown", lines: "ConditionCapability=CAP_NOPE", warn: "WARN:not a capability name"},
		{name: "needs update", lines: "ConditionNeedsUpdate=/etc", want: []string{"condition-needs-update = /etc"}},
		{name: "needs update negated", lines: "ConditionNeedsUpdate=!/var", want: []string{"condition-needs-update = !/var"}},
		{name: "needs update relative", lines: "ConditionNeedsUpdate=etc", warn: "WARN:must be absolute"},
		{name: "encrypted", lines: "ConditionPathIsEncrypted=/home", warn: "WARN:encrypted"},
		{name: "module", lines: "AssertKernelModuleLoaded=loop", want: []string{"assert-kernel-module-loaded = loop"}},
		{name: "module dash", lines: "ConditionKernelModuleLoaded=!nf-tables", want: []string{"condition-kernel-module-loaded = !nf-tables"}},
		{name: "pressure", lines: "ConditionMemoryPressure=20%", warn: "WARN:AT MOST"},
		{name: "io pressure assert", lines: "AssertIOPressure=10%", err: "AT MOST"},
	}
	if runtime.GOARCH == "amd64" || runtime.GOARCH == "386" {
		cases = append(cases,
			sdUnitCase{name: "cpufeature bare", lines: "ConditionCPUFeature=pclmul", want: []string{"condition-cpu-feature = pclmulqdq"}},
			sdUnitCase{name: "cpufeature unknown bare", lines: "ConditionCPUFeature=avx512f", warn: "WARN:does not know"})
	}
	if runtime.GOARCH == "arm64" {
		cases = append(cases,
			sdUnitCase{name: "cpufeature bare arm64", lines: "ConditionCPUFeature=ATOMICS", want: []string{"condition-cpu-feature = atomics"}},
			sdUnitCase{name: "cpufeature bare x86 on arm64", lines: "ConditionCPUFeature=avx2", warn: "WARN:does not know"})
	}
	runSDUnitCases(t, cases, "condition-", "assert-")
}

// The rewritten values evaluate on this machine as systemd's would; the
// ones passed on unchanged before did not (ConditionCPUs=1 was "== 1",
// false on any machine with two CPUs).
func TestSystemdConditionsEvaluateAsSystemd(t *testing.T) {
	arch := map[string]string{"amd64": "x86-64", "386": "x86", "arm64": "arm64", "riscv64": "riscv64",
		"ppc64le": "ppc64-le", "s390x": "s390x", "loong64": "loongarch64"}[runtime.GOARCH]
	lines := "ConditionCPUs=1\nConditionMemory=1K\nConditionKernelVersion=>=2.6\n" +
		"AssertKernelVersion=!<2.6\nConditionArchitecture=native"
	if arch != "" {
		lines += "\nConditionArchitecture=" + arch
	}
	p := writeUnit(t, t.TempDir(), "probe.service", "[Unit]\n"+lines+"\n[Service]\nExecStart=/bin/true\n")
	desc, err := SystemdUnitToServiceDescription(p, "probe")
	if err != nil {
		t.Fatal(err)
	}
	want := 4
	if arch != "" {
		want++
	}
	if len(desc.Predicates) != want {
		t.Fatalf("%d predicates, want %d: %v", len(desc.Predicates), want, desc.Predicates)
	}
	for _, pr := range desc.Predicates {
		if ok, why := pr.Evaluate(); !ok {
			t.Errorf("%s does not hold here: %s", pr, why)
		}
	}
	if !desc.Predicates[3].IsAssert || !desc.Predicates[3].Negate || desc.Predicates[3].Kind != service.PredKernelVersion {
		t.Errorf("AssertKernelVersion=!<2.6 parsed as %+v", desc.Predicates[3])
	}
}

// A "$" reaches the predicate as written, though the emitted file must
// escape it from slinit's own expansion.
func TestSystemdConditionDollarRoundTrip(t *testing.T) {
	p := writeUnit(t, t.TempDir(), "probe.service",
		"[Unit]\nConditionEnvironment=!FOO=$bar\n[Service]\nExecStart=/bin/true\n")
	desc, err := SystemdUnitToServiceDescription(p, "probe")
	if err != nil {
		t.Fatal(err)
	}
	if len(desc.Predicates) != 1 {
		t.Fatalf("predicates %v", desc.Predicates)
	}
	pr := desc.Predicates[0]
	if pr.Kind != service.PredEnvironment || pr.Param != "FOO=$bar" || !pr.Negate || pr.IsAssert {
		t.Errorf("parsed as %+v", pr)
	}
}

func TestSystemdUnitStartLimitAndActions(t *testing.T) {
	keys := []string{"restart-limit-", "start-limit-action", "failure-action", "success-action",
		"reboot-argument", "job-timeout-sec"}
	runSDUnitCases(t, []sdUnitCase{
		{name: "interval span", lines: "StartLimitIntervalSec=5min", want: []string{"restart-limit-interval = 300"}},
		{name: "interval old name", lines: "StartLimitInterval=10", want: []string{"restart-limit-interval = 10"}},
		{name: "interval infinity", lines: "StartLimitIntervalSec=infinity", want: []string{"restart-limit-interval = inf"}},
		{name: "interval off", lines: "StartLimitIntervalSec=0", want: []string{"restart-limit-interval = 0"}},
		{name: "interval junk", lines: "StartLimitIntervalSec=soon", warn: "WARN:time span"},
		{name: "burst", lines: "StartLimitBurst=5", want: []string{"restart-limit-count = 5"}, warn: "NOTE:automatic restarts"},
		{name: "burst off", lines: "StartLimitBurst=0", want: []string{"restart-limit-count = 0"}},
		{name: "burst negative", lines: "StartLimitBurst=-1", warn: "WARN:not a count"},
		{name: "burst last wins", lines: "StartLimitBurst=3\nStartLimitBurst=7", want: []string{"restart-limit-count = 7"}, warn: "NOTE:automatic"},
		{name: "start limit action", lines: "StartLimitAction=reboot-force", want: []string{"start-limit-action = reboot"}, warn: "NOTE:stops the services first"},
		{name: "kexec", lines: "StartLimitAction=kexec", warn: "WARN:no kexec action"},
		{name: "failure action", lines: "FailureAction=poweroff", want: []string{"failure-action = poweroff"}},
		{name: "soft-reboot", lines: "FailureAction=soft-reboot-force", warn: "WARN:no soft-reboot action"},
		{name: "invalid keeps the earlier", lines: "FailureAction=reboot\nFailureAction=bogus", want: []string{"failure-action = reboot"}, warn: "WARN:bogus"},
		{name: "success action", lines: "SuccessAction=exit", want: []string{"success-action = exit"}, warn: "NOTE:stopped by hand"},
		{name: "success none", lines: "SuccessAction=none", want: []string{"success-action = none"}},
		{name: "reboot argument", lines: "RebootArgument=fw-update", want: []string{"reboot-argument = fw-update"}, warn: "NOTE:reboot(2)"},
		{name: "reboot argument reset", lines: "RebootArgument=a\nRebootArgument=", warn: "NOTE:reboot(2)"},
		{name: "reboot argument dollar", lines: "RebootArgument=a$b", want: []string{"reboot-argument = a$$b"}, warn: "NOTE:reboot(2)"},
		{name: "job timeout", lines: "JobTimeoutSec=2min", want: []string{"job-timeout-sec = 120"}, warn: "NOTE:marks it failed"},
		{name: "job timeout infinity", lines: "JobTimeoutSec=infinity", want: []string{"job-timeout-sec = 0"}},
		{name: "job timeout zero", lines: "JobTimeoutSec=0", want: []string{"job-timeout-sec = 0"}},
		{name: "job running timeout", lines: "JobRunningTimeoutSec=10", warn: "NOTE:start-timeout"},
		{name: "job timeout action", lines: "JobTimeoutAction=reboot-force", warn: "NOTE:failure-action"},
		{name: "survive final kill", lines: "SurviveFinalKillSignal=yes", warn: "NOTE:kill sweep"},
		{name: "survive final kill default", lines: "SurviveFinalKillSignal=no"},
		{name: "exit status", lines: "FailureActionExitStatus=3", warn: "NOTE:exit status"},
		{name: "ignored", lines: "AllowIsolate=yes\nIgnoreOnSnapshot=yes\nCollectMode=inactive-or-failed\nSourcePath=/etc/fstab\nTags=a.b"},
	}, keys...)
}

func TestSystemdUnitActionsParse(t *testing.T) {
	p := writeUnit(t, t.TempDir(), "probe.service", "[Unit]\n"+
		"StartLimitIntervalSec=1min\nStartLimitBurst=4\nStartLimitAction=poweroff-immediate\n"+
		"FailureAction=reboot\nSuccessAction=halt\nRebootArgument=x\nJobTimeoutSec=90s\n"+
		"[Service]\nExecStart=/bin/true\n")
	desc, err := SystemdUnitToServiceDescription(p, "probe")
	if err != nil {
		t.Fatal(err)
	}
	if desc.RestartInterval != time.Minute || desc.RestartLimitCount != 4 || !desc.RestartLimitCountSet {
		t.Errorf("restart limit %v/%d", desc.RestartInterval, desc.RestartLimitCount)
	}
	if desc.StartLimitAction != service.ActionPoweroff || desc.FailureAction != service.ActionReboot ||
		desc.SuccessAction != service.ActionHalt {
		t.Errorf("actions %v %v %v", desc.StartLimitAction, desc.FailureAction, desc.SuccessAction)
	}
	if desc.RebootArgument != "x" || desc.JobTimeoutSec != 90*time.Second {
		t.Errorf("reboot argument %q, job timeout %v", desc.RebootArgument, desc.JobTimeoutSec)
	}
}

// Every Condition*/Assert* the file registers is one systemd defines: a
// misspelt or invented name would be classified without ever being used.
func TestSystemdConditionKeysExist(t *testing.T) {
	known := map[string]bool{}
	for _, d := range systemdDirectives(t) {
		if d[0] == "Unit" {
			known[d[1]] = true
		}
	}
	for kind, k := range sdConditionKinds {
		names := []string{"Condition" + kind}
		if !k.noAssert {
			names = append(names, "Assert"+kind)
		}
		for _, n := range names {
			if !known[n] {
				t.Errorf("%s is registered but systemd has no such directive", n)
			}
		}
	}
	// And every one systemd has is a kind here.
	for n := range known {
		for _, pre := range []string{"Condition", "Assert"} {
			if kind, ok := strings.CutPrefix(n, pre); ok {
				if _, ok := sdConditionKinds[kind]; !ok {
					t.Errorf("%s has no entry in sdConditionKinds", n)
				}
			}
		}
	}
}

func TestSDParseSize(t *testing.T) {
	for in, want := range map[string]uint64{"512": 512, "1K": 1024, "1.5G": 3 << 29, "2 M": 2 << 20, "1B": 1} {
		if got, err := ucParseSize(in); err != nil || got != want {
			t.Errorf("ucParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "G", "1GB", "-1", "1g", "99999999999E"} {
		if _, err := ucParseSize(in); err == nil {
			t.Errorf("ucParseSize(%q) accepted", in)
		}
	}
}

// %v is the running kernel's release, as uname -r prints it.
func TestSpecifierKernelRelease(t *testing.T) {
	rel := kernelRelease()
	if rel == "" {
		t.Skip("no uname")
	}
	cfg := &SystemdConfig{svcName: "s"}
	got, warns := expandSpecifiers(cfg, "ConditionPathExists", "/usr/lib/modules/%v/sysctl.conf")
	if got != "/usr/lib/modules/"+rel+"/sysctl.conf" || len(warns) != 0 {
		t.Errorf("%q %v", got, warns)
	}
}
