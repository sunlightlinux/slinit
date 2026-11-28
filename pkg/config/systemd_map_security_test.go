package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/service"
)

// secConvert runs a [Service] body through the converter and the
// emitter. err is the converter's refusal.
func secConvert(t *testing.T, service string) (string, []SystemdWarning, error) {
	t.Helper()
	out, warns, err, _ := secConvertIn(t, service, "")
	return out, warns, err
}

// secConvertIn is secConvert with a drop-in holding dropin as its
// [Service] section (none when empty). It also loads the unit as the
// daemon's loader does, the converted description through validateDesc;
// loadErr is that load's refusal.
func secConvertIn(t *testing.T, body, dropin string) (out string, warns []SystemdWarning, err, loadErr error) {
	t.Helper()
	dir := t.TempDir()
	p := writeUnit(t, dir, "sec.service", "[Service]\nExecStart=/bin/sleep 1\n"+body)
	if dropin != "" {
		writeTree(t, filepath.Join(dir, "sec.service.d", "10-override.conf"), "[Service]\n"+dropin)
	}
	cfg, warns, err := ConvertSystemdUnitIn(p, []string{dir})
	if err != nil {
		return "", nil, err, nil
	}
	var b bytes.Buffer
	EmitSlinitFile(&b, cfg)
	desc, loadErr := SystemdUnitToServiceDescription(p, "sec", dir)
	if loadErr == nil {
		dl := NewDirLoader(service.NewServiceSet(&testConsumerLogger{}), nil)
		loadErr = dl.validateDesc("sec", desc)
	}
	return b.String(), warns, nil, loadErr
}

// secLoad loads the same body as the direct loader does.
func secLoad(t *testing.T, service string) *ServiceDescription {
	t.Helper()
	p := writeUnit(t, t.TempDir(), "sec.service", "[Service]\nExecStart=/bin/sleep 1\n"+service)
	d, err := SystemdUnitToServiceDescription(p, "sec")
	if err != nil {
		t.Fatalf("load:\n%s\n%v", service, err)
	}
	return d
}

// secWarned reports whether a warning of level holds every part.
func secWarned(warns []SystemdWarning, level string, parts ...string) bool {
	for _, w := range warns {
		if w.Level != level {
			continue
		}
		all := true
		for _, p := range parts {
			all = all && strings.Contains(w.Msg, p)
		}
		if all {
			return true
		}
	}
	return false
}

// secNotShared pretends / is a private mount, as on a host slinit booted.
func secNotShared(t *testing.T, shared bool) {
	old := secRootShared
	secRootShared = func() bool { return shared }
	t.Cleanup(func() { secRootShared = old })
}

type secCase struct {
	name    string
	service string
	dropin  string   // a drop-in's [Service] section, applied after service
	want    []string // lines the emitted file must hold
	absent  []string // lines it must not hold
	warn    string   // a WARN must contain this
	note    string   // a NOTE must contain this
	refused string   // the converter must refuse, with this
	quiet   bool     // no warning at all
}

func runSecCases(t *testing.T, cases []secCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, warns, err, loadErr := secConvertIn(t, c.service, c.dropin)
			if c.refused != "" {
				if err == nil || !strings.Contains(err.Error(), c.refused) {
					t.Fatalf("want refusal %q, got err=%v\n%s", c.refused, err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			// What the converter accepts, the loader must load.
			if loadErr != nil {
				t.Errorf("converted, but the loader refuses it: %v\n%s", loadErr, out)
			}
			for _, l := range c.want {
				if !strings.Contains(out, "\n"+l+"\n") {
					t.Errorf("missing %q in\n%s", l, out)
				}
			}
			for _, l := range c.absent {
				if strings.Contains(out, "\n"+l) {
					t.Errorf("unexpected %q in\n%s", l, out)
				}
			}
			if c.warn != "" && !secWarned(warns, "WARN", c.warn) {
				t.Errorf("no WARN with %q: %+v", c.warn, warns)
			}
			if c.note != "" && !secWarned(warns, "NOTE", c.note) {
				t.Errorf("no NOTE with %q: %+v", c.note, warns)
			}
			if c.quiet && len(warns) > 0 {
				t.Errorf("want no warning, got %+v", warns)
			}
		})
	}
}

// secLSMHost points the LSM probes at a fake /sys where each LSM is
// active or not.
func secLSMHost(t *testing.T, active bool) {
	t.Helper()
	old := [3]string{secAppArmorEnabled, secSELinuxDir, secSmackDir}
	t.Cleanup(func() { secAppArmorEnabled, secSELinuxDir, secSmackDir = old[0], old[1], old[2] })
	root := t.TempDir()
	secAppArmorEnabled = filepath.Join(root, "apparmor-enabled")
	secSELinuxDir = filepath.Join(root, "selinux")
	secSmackDir = filepath.Join(root, "smackfs")
	if !active {
		// The module is there but disabled (apparmor=0): systemd
		// reads the parameter, not whether the file exists.
		writeFile(t, secAppArmorEnabled, "N\n")
		return
	}
	writeFile(t, secAppArmorEnabled, "Y\n")
	writeTree(t, filepath.Join(secSELinuxDir, "enforce"), "1")
	if err := os.Mkdir(secSmackDir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// The LSM labels map onto slinit's. Where the converting host has no
// such LSM, a label is still emitted (the runner fails closed where it
// cannot apply it), and only the "-" form is left out, with a WARN.
func TestSystemdSecLSM(t *testing.T) {
	secLSMHost(t, true)
	runSecCases(t, []secCase{
		{name: "apparmor", service: "AppArmorProfile=my-profile\n", want: []string{"apparmor-switch = my-profile"}, quiet: true},
		{name: "apparmor dash", service: "AppArmorProfile=-my-profile\n",
			want: []string{"apparmor-switch = my-profile"}, note: "fails the start instead"},
		{name: "apparmor reset", service: "AppArmorProfile=a\nAppArmorProfile=\n", absent: []string{"apparmor-switch"}},
		{name: "apparmor last wins", service: "AppArmorProfile=a\nAppArmorProfile=b\n",
			want: []string{"apparmor-switch = b"}, absent: []string{"apparmor-switch = a"}},
		{name: "apparmor specifier", service: "AppArmorProfile=%N-prof\n", want: []string{"apparmor-switch = sec-prof"}},
		// A literal "%H" would name another profile.
		{name: "apparmor unknown specifier", service: "AppArmorProfile=%H-prof\n",
			refused: "AppArmorProfile=%H-prof confines the service, and slinit cannot apply it (it uses a specifier"},
		{name: "apparmor unknown specifier overridden", service: "AppArmorProfile=%H-prof\nAppArmorProfile=p\n",
			want: []string{"apparmor-switch = p"}},
		{name: "apparmor dash only", service: "AppArmorProfile=-\n", refused: "AppArmorProfile=- confines the service"},
		{name: "selinux", service: "SELinuxContext=-system_u:system_r:foo_t:s0\n",
			want: []string{"selinux-context = system_u:system_r:foo_t:s0"}, note: "selinux-context fails the start"},
		{name: "smack", service: "SmackProcessLabel=web\n", want: []string{"smack-process-label = web"}},
	})
	d := secLoad(t, "AppArmorProfile=p\nSELinuxContext=c\nSmackProcessLabel=s\n")
	if d.AppArmorSwitch != "p" || d.SELinuxContext != "c" || d.SMACKProcessLabel != "s" {
		t.Errorf("loaded %q %q %q", d.AppArmorSwitch, d.SELinuxContext, d.SMACKProcessLabel)
	}

	secLSMHost(t, false)
	runSecCases(t, []secCase{
		{name: "no apparmor", service: "AppArmorProfile=my-profile\n",
			want: []string{"apparmor-switch = my-profile"}, note: "AppArmor is not active on the host converting"},
		{name: "no selinux", service: "SELinuxContext=c\n", want: []string{"selinux-context = c"}, note: "SELinux is not active"},
		{name: "no apparmor dash", service: "AppArmorProfile=-my-profile\n",
			absent: []string{"apparmor-switch"}, warn: "AppArmor is not active"},
		{name: "no smack dash", service: "SmackProcessLabel=-l\n", absent: []string{"smack-process-label"}, warn: "SMACK is not active"},
	})
}

func TestSystemdSecNamespaces(t *testing.T) {
	secNotShared(t, false)
	runSecCases(t, []secCase{
		{name: "ipc", service: "PrivateIPC=yes\n", want: []string{"namespace-ipc = yes"}, quiet: true},
		{name: "ipc on", service: "PrivateIPC=on\n", want: []string{"namespace-ipc = yes"}},
		{name: "ipc no", service: "PrivateIPC=no\n", absent: []string{"namespace-ipc"}, quiet: true},
		{name: "ipc yes then no", service: "PrivateIPC=yes\nPrivateIPC=false\n", absent: []string{"namespace-ipc"}},
		{name: "ipc empty keeps", service: "PrivateIPC=yes\nPrivateIPC=\n", want: []string{"namespace-ipc = yes"}},
		{name: "ipc invalid", service: "PrivateIPC=maybe\n", refused: "PrivateIPC=maybe confines the service"},
		{name: "pids", service: "PrivatePIDs=yes\n", want: []string{"namespace-pid = yes"}, warn: "no /proc"},
		{name: "mounts", service: "PrivateMounts=yes\n", want: []string{"namespace-mount = yes"}, note: "private rather than slave"},
		{name: "mounts no", service: "PrivateMounts=no\n", absent: []string{"namespace-mount"}, quiet: true},
		// One saying no does not undo the other's namespace.
		{name: "mountflags then no", service: "MountFlags=slave\nPrivateMounts=no\n", want: []string{"namespace-mount = yes"}},
		{name: "mountflags private", service: "MountFlags=private\n", want: []string{"namespace-mount = yes"}, quiet: true},
		{name: "mountflags shared", service: "MountFlags=shared\n", absent: []string{"namespace-mount"}, quiet: true},
		{name: "mountflags bogus", service: "MountFlags=rshared\n", refused: "MountFlags=rshared confines the service"},
		// The other order, and the last assignment of each deciding.
		{name: "no then mountflags", service: "PrivateMounts=no\nMountFlags=slave\n", want: []string{"namespace-mount = yes"}},
		{name: "mounts yes then no", service: "PrivateMounts=yes\nPrivateMounts=no\n", absent: []string{"namespace-mount"}},
		{name: "mounts yes then auto", service: "PrivateMounts=yes\nPrivateMounts=\n", absent: []string{"namespace-mount"}},
		{name: "mounts no in a drop-in", service: "PrivateMounts=yes\n", dropin: "PrivateMounts=no\n",
			absent: []string{"namespace-mount"}},
		{name: "mountflags slave then shared", service: "MountFlags=slave\nMountFlags=shared\n", absent: []string{"namespace-mount"}},
		{name: "mountflags shared, mounts yes", service: "MountFlags=slave\nPrivateMounts=yes\nMountFlags=shared\n",
			want: []string{"namespace-mount = yes"}},
		{name: "ipc no then yes", service: "PrivateIPC=false\nPrivateIPC=yes\n", want: []string{"namespace-ipc = yes"}},
		{name: "ipc no in a drop-in", service: "PrivateIPC=yes\n", dropin: "PrivateIPC=no\n", absent: []string{"namespace-ipc"}},
		// An invalid line systemd ignores: harmless after a yes, and
		// after a later valid line; refused where it may have meant yes.
		{name: "ipc invalid after yes", service: "PrivateIPC=yes\nPrivateIPC=maybe\n",
			want: []string{"namespace-ipc = yes"}, warn: "PrivateIPC=maybe ignored: not a boolean"},
		{name: "ipc invalid then no", service: "PrivateIPC=maybe\nPrivateIPC=no\n", absent: []string{"namespace-ipc"}},
		{name: "ipc invalid after no", service: "PrivateIPC=no\nPrivateIPC=maybe\n", refused: "PrivateIPC=maybe confines"},
		{name: "mountflags bogus after private", service: "MountFlags=private\nMountFlags=rshared\n",
			want: []string{"namespace-mount = yes"}, warn: "MountFlags=rshared ignored"},
	})
	d := secLoad(t, "PrivateIPC=yes\nPrivatePIDs=yes\nPrivateMounts=yes\n")
	if !d.NamespaceIPC || !d.NamespacePID || !d.NamespaceMount {
		t.Errorf("loaded ipc=%v pid=%v mount=%v", d.NamespaceIPC, d.NamespacePID, d.NamespaceMount)
	}

	// Where / is shared, the namespace leaks the service's mounts to
	// the host: a WARN, not a note.
	secNotShared(t, true)
	runSecCases(t, []secCase{
		{name: "shared host", service: "MountFlags=private\n", want: []string{"namespace-mount = yes"}, warn: "propagate back to the host"},
		{name: "shared host mounts", service: "PrivateMounts=1\n", warn: "propagate back to the host"},
	})
}

func TestSystemdSecPaths(t *testing.T) {
	src := t.TempDir()
	runSecCases(t, []secCase{
		{name: "bind", service: "BindPaths=" + src + " " + src + ":/srv/x:rbind\n",
			want: []string{"bind-paths = " + src + ":" + src + " " + src + ":/srv/x"}, quiet: true},
		{name: "bind missing optional", service: "BindPaths=-/nonexistent/sec\n", absent: []string{"bind-paths"},
			note: "decided when the unit was converted"},
		{name: "bind optional present", service: "BindPaths=-" + src + "\n", want: []string{"bind-paths = " + src + ":" + src},
			note: "the start fails if it is gone"},
		{name: "bind norbind", service: "BindPaths=" + src + ":/srv/x:norbind\n", absent: []string{"bind-paths"}, warn: "recursively only"},
		{name: "bind reset", service: "BindPaths=" + src + "\nBindPaths=\n", absent: []string{"bind-paths"}},
		{name: "bind specifier", service: "BindPaths=" + src + ":%t/%N\n", want: []string{"bind-paths = " + src + ":/run/sec"}},
		{name: "ro dirs", service: "ReadOnlyDirectories=/usr /etc\n", want: []string{"read-only-paths = /usr /etc"}},
		{name: "rw dirs", service: "ReadWriteDirectories=/var/lib/x\n", want: []string{"read-write-paths = /var/lib/x"}},
		{name: "inaccessible dirs", service: "InaccessibleDirectories=-/nonexistent/sec\n",
			want: []string{"inaccessible-paths = /nonexistent/sec"}},
		// The alias names itself in its warning.
		{name: "alias warning", service: "ReadOnlyDirectories=relative\n", warn: "ReadOnlyDirectories: \"relative\""},
		// Same list in systemd: the alias's reset empties the current
		// name's entries too.
		{name: "alias reset", service: "ReadOnlyPaths=/usr\nReadOnlyDirectories=\n", absent: []string{"read-only-paths"}},
		{name: "alias specifier", service: "ReadWriteDirectories=%S/%N\n", want: []string{"read-write-paths = /var/lib/sec"}},
	})
	d := secLoad(t, "BindPaths="+src+":/srv/x\nReadOnlyDirectories=/usr\nReadOnlyPaths=/etc\n")
	if len(d.BindPaths) != 1 || d.BindPaths[0] != src+":/srv/x" {
		t.Errorf("BindPaths = %q", d.BindPaths)
	}
	if strings.Join(d.ReadOnlyPaths, " ") != "/usr /etc" {
		t.Errorf("ReadOnlyPaths = %q", d.ReadOnlyPaths)
	}
}

func TestSystemdSecDirectoryModes(t *testing.T) {
	runSecCases(t, []secCase{
		{name: "runtime", service: "RuntimeDirectoryMode=0750\n", want: []string{"runtime-directory-mode = 0750"}, quiet: true},
		{name: "state short", service: "StateDirectoryMode=700\n", want: []string{"state-directory-mode = 0700"}},
		{name: "cache", service: "CacheDirectoryMode=0755\n", want: []string{"cache-directory-mode = 0755"}},
		{name: "logs", service: "LogsDirectoryMode=0640\n", want: []string{"logs-directory-mode = 0640"}},
		{name: "config", service: "ConfigurationDirectoryMode=0500\n", want: []string{"configuration-directory-mode = 0500"}},
		// setgid dropped: never wider than asked.
		{name: "setgid", service: "StateDirectoryMode=2770\n", want: []string{"state-directory-mode = 0770"}, warn: "permission bits only"},
		// sticky on a directory only the owner writes changes nothing.
		{name: "sticky owner", service: "StateDirectoryMode=1700\n", want: []string{"state-directory-mode = 0700"}},
		// sticky where others write: dropping it would let them remove
		// each other's files, so the mode is not applied at all.
		{name: "sticky shared", service: "RuntimeDirectoryMode=1777\n", absent: []string{"runtime-directory-mode"}, warn: "sticky"},
		{name: "not octal", service: "RuntimeDirectoryMode=0789\n", absent: []string{"runtime-directory-mode"}, warn: "no octal mode"},
		{name: "too big", service: "RuntimeDirectoryMode=17777\n", absent: []string{"runtime-directory-mode"}, warn: "no octal mode"},
		{name: "last wins", service: "RuntimeDirectoryMode=0700\nRuntimeDirectoryMode=0750\n",
			want: []string{"runtime-directory-mode = 0750"}, absent: []string{"runtime-directory-mode = 0700"}},
		{name: "preserve restart", service: "RuntimeDirectoryPreserve=restart\n", want: []string{"runtime-directory-preserve = restart"}},
		{name: "preserve true", service: "RuntimeDirectoryPreserve=true\n", want: []string{"runtime-directory-preserve = yes"}},
		{name: "preserve off", service: "RuntimeDirectoryPreserve=off\n", want: []string{"runtime-directory-preserve = no"}},
		{name: "preserve on-success", service: "RuntimeDirectoryPreserve=on-success\n",
			absent: []string{"runtime-directory-preserve"}, warn: "expected yes, no or restart"},
	})
	d := secLoad(t, "RuntimeDirectory=sec\nRuntimeDirectoryMode=0750\nRuntimeDirectoryPreserve=restart\n")
	if d.RuntimeDirMode == nil || *d.RuntimeDirMode != 0o750 || d.RuntimeDirPreserve != 2 {
		t.Errorf("loaded mode=%v preserve=%d", d.RuntimeDirMode, d.RuntimeDirPreserve)
	}
}

// DynamicUser= brings the sandbox systemd forces on such a service,
// but for ProtectSystem=strict, which slinit cannot give without
// breaking the service's own directories.
func TestSystemdSecDynamicUser(t *testing.T) {
	runSecCases(t, []secCase{
		{name: "yes", service: "DynamicUser=yes\n",
			want: []string{"dynamic-user = yes", "remove-ipc = yes", "restrict-suidsgid = yes", "private-tmp = yes",
				"protect-home = read-only", "options = no-new-privs"},
			absent: []string{"protect-system"}, warn: "ProtectSystem=strict"},
		// Forced over what came before, as unit_patch_contexts does.
		{name: "forced", service: "PrivateTmp=no\nRestrictSUIDSGID=no\nDynamicUser=yes\n",
			want:   []string{"private-tmp = yes", "restrict-suidsgid = yes"},
			absent: []string{"private-tmp = no", "restrict-suidsgid = no"}},
		// ProtectHome= is only raised from no.
		{name: "home kept", service: "ProtectHome=tmpfs\nDynamicUser=yes\n",
			want: []string{"protect-home = tmpfs"}, absent: []string{"protect-home = read-only"}},
		{name: "no", service: "DynamicUser=no\n", absent: []string{"dynamic-user", "remove-ipc"}, quiet: true},
		{name: "chroot", service: "RootDirectory=/srv/jail\nDynamicUser=yes\n",
			want: []string{"dynamic-user = yes"}, absent: []string{"private-tmp", "protect-home"}, warn: "cannot combine chroot"},
		{name: "invalid", service: "DynamicUser=sometimes\n", refused: "DynamicUser=sometimes confines the service"},
		{name: "remove-ipc kept", service: "DynamicUser=yes\nRemoveIPC=no\n", want: []string{"remove-ipc = yes"}},
		// Forced over the lines after it too, and those of a drop-in:
		// unit_patch_contexts runs once the whole unit is read.
		{name: "forced over later lines",
			service: "DynamicUser=yes\nPrivateTmp=no\nProtectHome=no\nNoNewPrivileges=no\nRestrictSUIDSGID=no\nRemoveIPC=no\n",
			want: []string{"private-tmp = yes", "protect-home = read-only", "options = no-new-privs",
				"restrict-suidsgid = yes", "remove-ipc = yes"},
			absent: []string{"private-tmp = no", "protect-home = no", "restrict-suidsgid = no"}},
		{name: "forced over a drop-in", service: "DynamicUser=yes\n",
			dropin: "PrivateTmp=no\nProtectHome=false\nNoNewPrivileges=no\nRestrictSUIDSGID=no\n",
			want:   []string{"private-tmp = yes", "protect-home = read-only", "options = no-new-privs", "restrict-suidsgid = yes"},
			absent: []string{"private-tmp = no", "protect-home = false", "restrict-suidsgid = no"}},
		{name: "home kept, after", service: "DynamicUser=yes\nProtectHome=tmpfs\n",
			want: []string{"protect-home = tmpfs"}, absent: []string{"protect-home = read-only"}},
		// RootDirectory= after DynamicUser=, or in a drop-in, must not
		// leave private-tmp beside chroot: the loader refuses the pair.
		{name: "chroot after", service: "DynamicUser=yes\nRootDirectory=/srv/jail\n",
			want: []string{"dynamic-user = yes"}, absent: []string{"private-tmp", "protect-home"}, warn: "cannot combine chroot"},
		{name: "chroot in a drop-in", service: "DynamicUser=yes\n", dropin: "RootDirectory=/srv/jail\n",
			want: []string{"dynamic-user = yes", "chroot = /srv/jail"}, absent: []string{"private-tmp", "protect-home"}},
		// A drop-in turning it off takes the implied settings along.
		{name: "no in a drop-in", service: "DynamicUser=yes\n", dropin: "DynamicUser=no\n",
			absent: []string{"dynamic-user", "remove-ipc", "restrict-suidsgid", "private-tmp", "protect-home", "options = no-new-privs"}},
		{name: "invalid after yes", service: "DynamicUser=yes\nDynamicUser=sometimes\n",
			want: []string{"dynamic-user = yes", "private-tmp = yes"}, warn: "DynamicUser=sometimes ignored"},
		{name: "invalid before yes", service: "DynamicUser=sometimes\nDynamicUser=yes\n", want: []string{"dynamic-user = yes"}},
		{name: "invalid after no", service: "DynamicUser=no\nDynamicUser=sometimes\n", refused: "DynamicUser=sometimes confines"},
		{name: "empty ignored", service: "DynamicUser=yes\nDynamicUser=\n", want: []string{"dynamic-user = yes"}},
	})
	d := secLoad(t, "DynamicUser=yes\nPrivateTmp=no\nNoNewPrivileges=no\nRestrictSUIDSGID=no\n")
	if !d.DynamicUser || !d.RemoveIPC || !d.RestrictSUIDSGID || !d.PrivateTmp || !d.NoNewPrivs || d.ProtectHome != "read-only" {
		t.Errorf("loaded %+v", d)
	}
}

// RootDirectory= with BindPaths= or an LSM label: slinit-runner cannot
// apply either inside a chroot, and the loader refuses the pair. The
// bind mounts are dropped with a WARN; a label is never dropped
// silently, so the converter refuses the unit and names both
// directives. Whichever comes first, and through a drop-in.
func TestSystemdSecChroot(t *testing.T) {
	src := t.TempDir()
	secLSMHost(t, true)
	runSecCases(t, []secCase{
		{name: "bind then chroot", service: "BindPaths=" + src + "\nRootDirectory=/srv/jail\n",
			want: []string{"chroot = /srv/jail"}, absent: []string{"bind-paths"}, warn: "BindPaths= with RootDirectory=/srv/jail"},
		{name: "chroot then bind", service: "RootDirectory=/srv/jail\nBindPaths=" + src + "\n",
			absent: []string{"bind-paths"}, warn: "BindPaths= with RootDirectory=/srv/jail"},
		{name: "chroot in a drop-in", service: "BindPaths=" + src + "\n", dropin: "RootDirectory=/srv/jail\n",
			absent: []string{"bind-paths"}, warn: "BindPaths= with RootDirectory="},
		{name: "chroot reset in a drop-in", service: "RootDirectory=/srv/jail\nBindPaths=" + src + "\n",
			dropin: "RootDirectory=\n", want: []string{"bind-paths = " + src + ":" + src}, absent: []string{"chroot"}},
		{name: "apparmor then chroot", service: "AppArmorProfile=p\nRootDirectory=/srv/jail\n",
			refused: "AppArmorProfile=p together with RootDirectory=/srv/jail"},
		{name: "chroot then apparmor", service: "RootDirectory=/srv/jail\nAppArmorProfile=p\n",
			refused: "AppArmorProfile=p together with RootDirectory=/srv/jail"},
		{name: "apparmor, chroot in a drop-in", service: "AppArmorProfile=p\n", dropin: "RootDirectory=/srv/jail\n",
			refused: "AppArmorProfile=p together with RootDirectory="},
		{name: "selinux and chroot", service: "SELinuxContext=-c\nRootDirectory=/srv/jail\n",
			refused: "SELinuxContext=-c together with RootDirectory="},
		{name: "smack and chroot", service: "SmackProcessLabel=l\nRootDirectory=/srv/jail\n",
			refused: "SmackProcessLabel=l together with RootDirectory="},
		{name: "label reset in a drop-in", service: "AppArmorProfile=p\nRootDirectory=/srv/jail\n", dropin: "AppArmorProfile=\n",
			want: []string{"chroot = /srv/jail"}, absent: []string{"apparmor-switch"}},
	})
	// "-" on a host without the LSM: left out, so nothing to conflict.
	secLSMHost(t, false)
	runSecCases(t, []secCase{
		{name: "lenient label, no LSM", service: "AppArmorProfile=-p\nRootDirectory=/srv/jail\n",
			want: []string{"chroot = /srv/jail"}, absent: []string{"apparmor-switch"}, warn: "AppArmor is not active"},
	})
}

func TestSystemdSecProcessAttributes(t *testing.T) {
	native := secPersonalities[runtime.GOARCH]
	cases := []secCase{
		{name: "removeipc", service: "RemoveIPC=yes\n", want: []string{"remove-ipc = yes"}, note: "other services of the same user"},
		{name: "removeipc no", service: "RemoveIPC=yes\nRemoveIPC=no\n", absent: []string{"remove-ipc"}},
		{name: "removeipc bogus", service: "RemoveIPC=perhaps\n", absent: []string{"remove-ipc"}, warn: "ignored"},
		{name: "securebits", service: "SecureBits=keep-caps noroot-locked\n", want: []string{"securebits = keep-caps noroot-locked"}, quiet: true},
		// A scalar in systemd: the second replaces the first.
		{name: "securebits replace", service: "SecureBits=keep-caps\nSecureBits=noroot\n",
			want: []string{"securebits = noroot"}, absent: []string{"securebits = keep-caps"}},
		{name: "securebits unknown", service: "SecureBits=noroot bogus\n", want: []string{"securebits = noroot"}, warn: "bogus unknown"},
		{name: "securebits case", service: "SecureBits=NoRoot\n", absent: []string{"securebits"}, warn: "NoRoot unknown"},
		{name: "securebits reset", service: "SecureBits=noroot\nSecureBits=\n", absent: []string{"securebits"}},
		{name: "personality bogus", service: "Personality=sparc\n", absent: []string{"personality"}, warn: "Personality=sparc ignored"},
		{name: "keyring inherit", service: "KeyringMode=inherit\n", quiet: true},
		{name: "keyring private", service: "KeyringMode=private\n", want: []string{"keyring-mode = private"}, quiet: true},
		{name: "keyring shared", service: "KeyringMode=shared\n", want: []string{"keyring-mode = shared"}, quiet: true},
		{name: "keyring private then inherit", service: "KeyringMode=private\nKeyringMode=inherit\n", absent: []string{"keyring-mode"}},
		{name: "keyring bogus", service: "KeyringMode=public\n", warn: "expected inherit"},
		{name: "login env", service: "SetLoginEnvironment=yes\n", want: []string{"load-options = export-passwd-vars"}},
		{name: "login env no", service: "SetLoginEnvironment=yes\nSetLoginEnvironment=no\n", absent: []string{"load-options"}},
		{name: "login env reset", service: "SetLoginEnvironment=yes\nSetLoginEnvironment=\n", absent: []string{"load-options"}},
		{name: "rootdir start only", service: "RootDirectoryStartOnly=yes\n", quiet: true},
		{name: "rootdir start only no", service: "RootDirectoryStartOnly=no\n", note: "outside RootDirectory="},
	}
	if len(native) > 0 {
		cases = append(cases,
			secCase{name: "personality", service: "Personality=" + native[0] + "\n", want: []string{"personality = " + native[0]}},
			secCase{name: "personality reset", service: "Personality=" + native[0] + "\nPersonality=\n", absent: []string{"personality"}})
	}
	runSecCases(t, cases)
	d := secLoad(t, "RemoveIPC=yes\nSecureBits=noroot keep-caps\nSetLoginEnvironment=yes\n")
	if !d.RemoveIPC || d.Securebits != "noroot keep-caps" || !d.ExportPasswdVars {
		t.Errorf("loaded removeipc=%v securebits=%q exportpasswd=%v", d.RemoveIPC, d.Securebits, d.ExportPasswdVars)
	}
}

// A gap is a WARN when the service runs less confined for it, a NOTE
// otherwise, and nothing at all for a value that restates the default.
// Neither refuses the unit.
func TestSystemdSecGaps(t *testing.T) {
	for key, confine := range map[string]bool{
		"NoExecPaths": true, "RestrictFileSystems": true, "PrivateBPF": true,
		"NetworkNamespacePath": true, "IPCNamespacePath": true, "UserNamespacePath": true,
		"BindNetworkInterface": true, "RootImage": true, "RootMStack": true, "RootEphemeral": true,
		"ExecPaths": false, "MountAPIVFS": false, "BindLogSockets": false, "CoredumpReceive": false,
		"PAMName": false, "LUOSession": false, "ExtensionImages": false, "ExtensionDirectories": false,
		"MountImages": false, "MountImagePolicy": false, "ExtensionImagePolicy": false,
		"RootImageOptions": false, "RootImagePolicy": false, "RootHash": false,
		"RootHashSignature": false, "RootVerity": false,
	} {
		g, ok := sdServiceNoEquivalent[key]
		if !ok || g.confine != confine {
			t.Errorf("%s: gap=%v confine=%v, want confine=%v", key, ok, g.confine, confine)
			continue
		}
		_, warns, err := secConvert(t, key+"=/srv/x\n")
		if err != nil {
			t.Errorf("%s refused: %v", key, err)
		}
		level := map[bool]string{true: "WARN", false: "NOTE"}[confine]
		if !secWarned(warns, level, key+"=/srv/x has no slinit equivalent") {
			t.Errorf("%s: want a %s, got %+v", key, level, warns)
		}
		if _, warns, _ := secConvert(t, key+"=no\n"); len(warns) > 0 {
			t.Errorf("%s=no: want silence, got %+v", key, warns)
		}
	}
	if _, ok := sdServiceIgnored["Capabilities"]; !ok {
		t.Error("Capabilities= is not ignored")
	}
	if out, warns, err := secConvert(t, "Capabilities=cap_net_raw=eip\n"); err != nil || len(warns) > 0 || strings.Contains(out, "capabilities") {
		t.Errorf("Capabilities=: err=%v warns=%+v\n%s", err, warns, out)
	}
}

// The mountinfo probe reads the last mount on /, and its optional
// fields only: a "shared:" after the "-" (here the source) does not
// count.
func TestSystemdSecRootSharedProbe(t *testing.T) {
	for _, c := range []struct {
		name, table string
		want        bool
	}{
		{"systemd host", "22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw\n", true},
		{"slinit host", "22 1 8:2 / / rw,relatime - ext4 /dev/sda2 rw\n", false},
		{"slave", "22 1 8:2 / / rw master:1 - ext4 /dev/sda2 rw\n", false},
		{"overmounted", "22 1 8:2 / / rw shared:1 - ext4 /dev/sda2 rw\n30 22 0:5 / / rw - overlay o rw\n", false},
		{"other mounts only", "23 22 0:5 / /proc rw shared:2 - proc proc rw\n22 1 8:2 / / rw - ext4 /dev/sda2 rw\n", false},
		{"source after the dash", "22 1 0:9 / / rw - tmpfs shared:9 rw\n", false},
		{"no root", "23 22 0:5 / /proc rw - proc proc rw\n", true},
	} {
		if got := secRootSharedIn(strings.NewReader(c.table)); got != c.want {
			t.Errorf("%s: shared=%v, want %v", c.name, got, c.want)
		}
	}
}
