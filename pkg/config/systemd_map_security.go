package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// The [Service] security and sandbox directives: LSM labels, namespaces,
// the per-service directory modes, DynamicUser= and the image family.
//
// Most of these confine the service. A mapping whose value slinit cannot
// take refuses the unit (sdConfine), the rule the capability and syscall
// folds follow. A directive slinit lacks is a gap, a WARN where its
// absence leaves the service less confined; it does not refuse the unit,
// for the reason PrivateDevices= and PrivateUsers= only warn: refusing
// would stop a great many upstream units from loading at all.

func init() {
	// The lines below record what the unit says in secState; secFinish
	// decides once the whole unit and its drop-ins are read, so that the
	// order of the lines does not change the result (DynamicUser= before
	// RootDirectory=, a drop-in saying PrivateMounts=no), as it does not
	// in systemd.
	registerFinisher(secFinish)

	// --- LSM labels ---
	for _, l := range secLSMs {
		registerService(l.directive, sdRule{custom: secLSMLine})
	}

	// --- Namespaces ---
	registerService("PrivateIPC", sdRule{custom: secFlagLine(func(s *secState) *secFlag { return &s.ipc }, false)})
	registerService("PrivatePIDs", sdRule{custom: secFlagLine(func(s *secState) *secFlag { return &s.pids }, false)})
	// PrivateMounts= is a tristate in systemd: empty is "auto", which
	// asks for no namespace of its own.
	registerService("PrivateMounts", sdRule{custom: secFlagLine(func(s *secState) *secFlag { return &s.mounts }, true)})
	registerService("MountFlags", sdRule{custom: secMountFlags})

	// --- Paths ---
	registerService("BindPaths", sdRule{custom: secBindPaths})
	// Deprecated spellings systemd still parses into the same lists
	// (config_parse_namespace_path_strv on the same fields), so they
	// go through the very case the current names do.
	registerService("ReadOnlyDirectories", sdRule{custom: secPathAlias("ReadOnlyPaths")})
	registerService("ReadWriteDirectories", sdRule{custom: secPathAlias("ReadWritePaths")})
	registerService("InaccessibleDirectories", sdRule{custom: secPathAlias("InaccessiblePaths")})

	// --- Per-service directories ---
	//
	// The modes are the permissions of directories the service owns,
	// not confinement of the process: a value slinit cannot take is
	// dropped with a warning, which leaves slinit's 0755, systemd's
	// default too.
	for sd, key := range map[string]string{
		"RuntimeDirectoryMode":       "runtime-directory-mode",
		"StateDirectoryMode":         "state-directory-mode",
		"CacheDirectoryMode":         "cache-directory-mode",
		"LogsDirectoryMode":          "logs-directory-mode",
		"ConfigurationDirectoryMode": "configuration-directory-mode",
	} {
		registerService(sd, sdRule{key: key, conv: secDirMode})
	}
	registerService("RuntimeDirectoryPreserve", sdRule{key: "runtime-directory-preserve", conv: secPreserve})

	// --- Identity and process attributes ---
	registerService("DynamicUser", sdRule{custom: secFlagLine(func(s *secState) *secFlag { return &s.dyn }, false)})
	registerService("RemoveIPC", sdRule{custom: secRemoveIPC})
	registerService("SecureBits", sdRule{custom: secSecureBits})
	registerService("Personality", sdRule{custom: secScalar(sdRule{key: "personality", conv: secPersonality})})
	registerService("KeyringMode", sdRule{custom: secKeyringMode})
	registerService("SetLoginEnvironment", sdRule{custom: secLoginEnvironment})
	registerService("RootDirectoryStartOnly", sdRule{custom: secRootDirectoryStartOnly})

	// --- Gaps that leave the service less confined (WARN) ---
	for sd, why := range map[string]string{
		"NoExecPaths": "slinit cannot mount paths noexec for one service; the service may execute " +
			"anything it can read. Remove the execute bits or mount the filesystem noexec",
		"RestrictFileSystems": "slinit cannot filter file access by filesystem type (systemd does it with BPF-LSM); " +
			"slinit's restrict-file-systems is a different control (it refuses the mount syscalls) and is not applied",
		"PrivateBPF": "slinit mounts no private bpffs; the service sees the host's /sys/fs/bpf",
		"NetworkNamespacePath": "slinit cannot join an existing network namespace; the service runs in the " +
			"host's (namespace-net = yes gives it a new, empty one instead)",
		"IPCNamespacePath": "slinit cannot join an existing IPC namespace; the service shares the host's " +
			"(namespace-ipc = yes gives it a new one instead)",
		"UserNamespacePath": "slinit cannot join an existing user namespace; the service runs in the host's",
		"BindNetworkInterface": "slinit cannot bind a service's sockets to an interface or VRF; the service " +
			"talks on every interface. Start it under `ip vrf exec` instead",
		"RootImage": "slinit mounts no disk images; the service runs on the host's root filesystem. " +
			"Unpack the image and use RootDirectory= (chroot) instead",
		"RootMStack": "slinit builds no overlay root from a mount stack; the service runs on the host's root filesystem",
		"RootEphemeral": "slinit chroots into the directory itself rather than a throwaway copy; " +
			"what the service writes there stays",
	} {
		sdServiceNoEquivalent[sd] = sdGap{confine: true, why: why}
	}

	// --- Gaps that leave the service no less confined (NOTE) ---
	for sd, why := range map[string]string{
		"ExecPaths": "slinit has no NoExecPaths= for it to make exceptions to; " +
			"nothing is mounted noexec, so these paths are executable anyway",
		"MountAPIVFS": "slinit mounts no /proc, /sys, /dev or /run inside a chroot; " +
			"a chrooted service needs them prepared in the jail",
		"BindLogSockets": "slinit binds no journal sockets into a service's namespace; " +
			"the service's stdout and stderr still reach slinit's logger",
		"CoredumpReceive": "slinit forwards no coredumps into containers; " +
			"systemd-coredump on the host keeps those of the service's containers",
		"PAMName": "slinit opens no PAM session for a service (it would need libpam); " +
			"set limits and environment with slinit's own rlimit-* and env-file",
		"LUOSession": "slinit hands out no Live Update Orchestrator sessions; " +
			"the service starts without the fd systemd would put in its fd store",
		"ExtensionImages": "slinit overlays no system extension images; " +
			"the service sees the host's /usr and /opt as they are",
		"ExtensionDirectories": "slinit overlays no system extension directories; " +
			"the service sees the host's /usr and /opt as they are",
		"MountImages": "slinit mounts no disk images into a service's namespace; " +
			"mount the image on the host and use bind-paths",
		"MountImagePolicy":     "slinit mounts no disk images, so there is no policy to apply",
		"ExtensionImagePolicy": "slinit overlays no extension images, so there is no policy to apply",
		"RootImageOptions":     "only qualifies RootImage=, which slinit does not support",
		"RootImagePolicy":      "only qualifies RootImage=, which slinit does not support",
		"RootHash":             "only qualifies RootImage= (dm-verity), which slinit does not support",
		"RootHashSignature":    "only qualifies RootImage= (dm-verity), which slinit does not support",
		"RootVerity":           "only qualifies RootImage= (dm-verity), which slinit does not support",
	} {
		sdServiceNoEquivalent[sd] = sdGap{confine: false, why: why}
	}

	// systemd itself has dropped it: config_parse_warn_compat with
	// DISABLED_LEGACY logs that support was removed and ignores it.
	// Its libcap text ("cap_net_raw=eip") is not what slinit's
	// capabilities takes either; AmbientCapabilities= and
	// CapabilityBoundingSet= replaced it.
	sdServiceIgnored["Capabilities"] = "removed from systemd, which ignores it with a warning"
}

// secBool reads systemd's parse_boolean vocabulary, which is wider than
// slinit's parseBool (no y/n/t/f/on/off there).
func secBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "yes", "y", "true", "t", "on":
		return true, nil
	case "0", "no", "n", "false", "f", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean", v)
}

// secValue is the value as the rules see it: quotes and blanks trimmed.
func secValue(val string) string { return strings.TrimSpace(trimQuotes(val)) }

// secRefuse records a confining directive slinit cannot apply.
func secRefuse(cfg *SystemdConfig, key, val string, err error) {
	cfg.errs = append(cfg.errs, fmt.Sprintf("%s=%s confines the service, and slinit cannot apply it (%v)", key, val, err))
}

// secForce sets key to value whatever earlier assignments said, also
// when a bespoke case appended it more than once.
func secForce(cfg *SystemdConfig, key, value string) {
	cfg.dropMapped(key)
	cfg.mapped = append(cfg.mapped, kvPair{key, value})
}

// secMappedValue is the value key ends up with in the emitted file: the
// last assignment, as Parse reads it.
func secMappedValue(cfg *SystemdConfig, key string) (string, bool) {
	v, ok := "", false
	for _, kv := range cfg.mapped {
		if kv.key == key {
			v, ok = kv.value, true
		}
	}
	return v, ok
}

// secScalar wraps a plain rule so an empty assignment resets the
// setting, as it does for a systemd scalar; applyRule only resets lists.
func secScalar(r sdRule) func(cfg *SystemdConfig, key, val string) []SystemdWarning {
	return func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		if secValue(val) == "" {
			cfg.dropMapped(r.key)
			return nil
		}
		return cfg.applyRule("Service", key, val, r)
	}
}

// --- Per-unit state ---

// secState is what the unit's lines said, for secFinish to decide on.
type secState struct {
	dyn, ipc, pids, mounts secFlag
	// mountFlags is MountFlags=: on for slave or private, want the one
	// asked for.
	mountFlags secFlag
	want       string
	// lsm holds the last assignment of each LSM directive, by
	// directive name; nil once an empty assignment reset it.
	lsm map[string]*secLabel
}

// secFlag is a boolean directive as systemd ends up with it: the last
// valid assignment wins and an invalid one is ignored. invalid keeps an
// invalid assignment made after the last valid one, for secSettle.
type secFlag struct {
	on       bool
	key, val string // the assignment that set on
	invalid  string // "Key=value", or ""
}

func secStateOf(cfg *SystemdConfig) *secState {
	return sdState(cfg, "security", func() *secState { return &secState{lsm: map[string]*secLabel{}} })
}

// secFlagLine records a boolean directive. emptyResets is the tristate
// PrivateMounts=, whose empty value is "auto" (off here); for the plain
// booleans systemd's parse_boolean refuses "" and the line is ignored.
func secFlagLine(field func(*secState) *secFlag, emptyResets bool) func(*SystemdConfig, string, string) []SystemdWarning {
	return func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		f := field(secStateOf(cfg))
		v := secValue(val)
		if v == "" {
			if emptyResets {
				*f = secFlag{}
			}
			return nil
		}
		on, err := secBool(v)
		if err != nil {
			f.invalid = key + "=" + val
			return nil
		}
		*f = secFlag{on: on, key: key, val: val}
		return nil
	}
}

// secSettle is the final value of a boolean directive. systemd ignores
// an invalid assignment and keeps the one before it. When that one
// confines the service anyway, ignoring the line costs nothing and is
// what systemd does: a WARN. When it does not, the line may have meant
// yes (DynamicUser=enabled), and running the service unconfined on a
// guess is the one outcome a confining directive must not have: the
// unit is refused, as a confining value slinit cannot take is.
func (f *secFlag) settle(cfg *SystemdConfig) []SystemdWarning {
	if f.invalid == "" {
		return nil
	}
	if f.on {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s ignored: not a boolean; systemd ignores it too "+
			"and keeps %s=%s", f.invalid, f.key, f.val)}}
	}
	cfg.errs = append(cfg.errs, fmt.Sprintf("%s confines the service, and slinit cannot apply it (not a boolean; "+
		"systemd ignores the line, which may have meant yes)", f.invalid))
	return nil
}

// --- LSM labels ---

// secLSM is one LSM label directive. All three apply the same way on
// both sides: apparmor-switch writes "exec <profile>" to
// /proc/self/attr/exec, which is what aa_change_onexec() does,
// selinux-context is setexeccon(), and smack-process-label writes the
// task's own label. The difference is what happens when the label
// cannot be applied:
//
//   - LSM not active: systemd skips the directive without a word;
//     slinit's runner fails the start. Whether it is active is a
//     question about the host the service runs on, which need not be
//     the one converting the unit (a build chroot, a container without
//     securityfs). So a label is emitted whatever this host says, and
//     the runner fails closed where it cannot apply it; only the "-"
//     form, where the unit itself allows running without the label, is
//     left out when this host has no such LSM, with a WARN.
//   - "-" with the LSM active (profile not loaded, context invalid):
//     systemd runs the service without the label; slinit fails the
//     start. Kept fail-closed, since the alternative is a service
//     running unconfined while its unit names a profile; a NOTE says so.
type secLSM struct {
	directive, key, name string
	active               func() bool
}

var secLSMs = []secLSM{
	{"AppArmorProfile", "apparmor-switch", "AppArmor", secAppArmorActive},
	{"SELinuxContext", "selinux-context", "SELinux", secSELinuxActive},
	{"SmackProcessLabel", "smack-process-label", "SMACK", secSmackActive},
}

// The files systemd's mac_*_use() look at. Variables so a test can
// point them at a file that is or is not there.
var (
	// mac_apparmor_use: the module parameter, a boolean ("Y").
	secAppArmorEnabled = "/sys/module/apparmor/parameters/enabled"
	// mac_selinux_use is libselinux's is_selinux_enabled(): selinuxfs
	// is mounted, which its enforce file shows.
	secSELinuxDir = "/sys/fs/selinux"
	// mac_smack_use: access("/sys/fs/smackfs/").
	secSmackDir = "/sys/fs/smackfs"
)

func secAppArmorActive() bool {
	b, err := os.ReadFile(secAppArmorEnabled)
	if err != nil {
		return false
	}
	on, err := secBool(strings.TrimSpace(string(b)))
	return err == nil && on
}

func secSELinuxActive() bool {
	_, err := os.Stat(filepath.Join(secSELinuxDir, "enforce"))
	return err == nil
}

func secSmackActive() bool {
	_, err := os.Stat(secSmackDir)
	return err == nil
}

// secLabel is the last assignment of one LSM directive.
type secLabel struct {
	val     string // as written, for messages
	label   string // specifiers expanded, "-" taken off
	lenient bool   // the "-" form
	err     error  // why slinit cannot apply it
}

// secLSMLine records AppArmorProfile=, SELinuxContext= or
// SmackProcessLabel=, "[-]label"; the last assignment wins and an empty
// one resets.
func secLSMLine(cfg *SystemdConfig, key, val string) []SystemdWarning {
	s := secStateOf(cfg)
	v := secValue(val)
	if v == "" {
		s.lsm[key] = nil
		return nil
	}
	l := &secLabel{val: val}
	exp, w := expandSpecifiers(cfg, key, v)
	l.label, l.lenient = strings.CutPrefix(exp, "-")
	switch {
	case len(w) > 0:
		// The literal "%H" would name another profile than the one
		// the unit means; the label cannot be applied as written.
		l.err = fmt.Errorf("it uses a specifier slinit cannot expand")
	case l.label == "":
		l.err = fmt.Errorf("no label after the \"-\"")
	}
	s.lsm[key] = l
	return nil
}

// secFinishLSM maps the LSM labels the unit ends up with.
func secFinishLSM(cfg *SystemdConfig, s *secState) []SystemdWarning {
	var warns []SystemdWarning
	for _, m := range secLSMs {
		l := s.lsm[m.directive]
		if l == nil {
			continue
		}
		if l.err != nil {
			secRefuse(cfg, m.directive, l.val, l.err)
			continue
		}
		active := m.active()
		if l.lenient && !active {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s not applied: %s is not active on the host "+
				"converting the unit, and with \"-\" the unit allows running without it. Where the service runs on a "+
				"host with %s, it runs unconfined", m.directive, l.val, m.name, m.name)})
			continue
		}
		if strings.TrimSpace(cfg.chroot) != "" {
			// slinit-runner applies the label through /proc after
			// the chroot, so the jail would decide whether the
			// transition happens; the loader refuses the pair.
			// Dropping the label would run the service unconfined.
			cfg.errs = append(cfg.errs, fmt.Sprintf("%s=%s together with RootDirectory=%s: slinit cannot apply an LSM "+
				"label to a chrooted service, and leaving the label out would run it unconfined", m.directive, l.val, cfg.chroot))
			continue
		}
		r := sdRule{key: m.key, class: sdConfine}
		warns = append(warns, cfg.applyRule("Service", m.directive, l.label, r)...)
		if l.lenient {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s: with \"-\" systemd runs the service "+
				"without the label when it cannot be applied; slinit's %s fails the start instead", m.directive, l.val, m.key)})
		} else if !active {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s: %s is not active on the host converting "+
				"the unit. Where it is not active when the service starts, slinit's %s fails the start, "+
				"where systemd skips the directive", m.directive, l.val, m.name, m.key)})
		}
	}
	return warns
}

// --- Namespaces ---

// secMountFlags records MountFlags=. slave and private each make
// systemd create a mount namespace (exec_needs_mount_namespace); shared,
// the default, and empty do not on their own. An unknown value systemd
// ignores, keeping the one before.
func secMountFlags(cfg *SystemdConfig, key, val string) []SystemdWarning {
	s := secStateOf(cfg)
	switch v := strings.ToLower(secValue(val)); v {
	case "", "shared":
		s.mountFlags, s.want = secFlag{key: key, val: val}, ""
	case "slave", "private":
		s.mountFlags, s.want = secFlag{on: true, key: key, val: val}, v
	default:
		s.mountFlags.invalid = key + "=" + val
	}
	return nil
}

// secFinishNamespaces maps PrivateIPC=, PrivatePIDs=, PrivateMounts=
// and MountFlags=. The last two each ask for a mount namespace, and
// systemd makes one when either does: PrivateMounts=no does not undo
// MountFlags=slave.
func secFinishNamespaces(cfg *SystemdConfig, s *secState) []SystemdWarning {
	var warns []SystemdWarning
	for _, f := range []*secFlag{&s.ipc, &s.pids, &s.mounts, &s.mountFlags} {
		warns = append(warns, f.settle(cfg)...)
	}
	if s.ipc.on {
		cfg.setMapped("namespace-ipc", "yes")
	}
	if s.pids.on {
		cfg.setMapped("namespace-pid", "yes")
		// systemd mounts a /proc of the new namespace (MountAPIVFS=
		// is implied); slinit's namespace-pid only clones, so /proc
		// is still the host's.
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s -> namespace-pid = yes; slinit mounts no /proc "+
			"of the new PID namespace, so the service still sees every host process under /proc", s.pids.key, s.pids.val)})
	}
	switch {
	case s.mountFlags.on:
		cfg.setMapped("namespace-mount", "yes")
		warns = append(warns, secPropagation(s.mountFlags.key, s.mountFlags.val, s.want)...)
	case s.mounts.on:
		cfg.setMapped("namespace-mount", "yes")
		warns = append(warns, secPropagation(s.mounts.key, s.mounts.val, "slave")...)
	}
	return warns
}

// secPropagation says how slinit's mount namespace differs from the
// propagation the unit asked for. slinit's namespace-mount only clones:
// every mount keeps the propagation it has on the host. slinit never
// makes / shared, so on a host it booted the namespace is private: no
// mount the service makes reaches the host (what slave and private
// promise), but no mount the host makes later reaches the service
// either, which slave would allow. Where / is shared (a host booted by
// systemd, a container), the service's mounts propagate back to the
// host, which is exactly what the directive exists to prevent.
func secPropagation(key, val, want string) []SystemdWarning {
	if secRootShared() {
		return []SystemdWarning{{"WARN", fmt.Sprintf("%s=%s -> namespace-mount = yes; / is a shared mount on this host "+
			"and slinit does not make the namespace %s, so mounts the service makes propagate back to the host", key, val, want)}}
	}
	if want == "private" {
		return nil
	}
	return []SystemdWarning{{"NOTE", fmt.Sprintf("%s=%s -> namespace-mount = yes; the namespace is private rather than slave: "+
		"mounts the host makes after the start do not reach the service", key, val)}}
}

// secRootShared reports whether / is a shared mount in this mount
// namespace. Unreadable counts as shared: the warning is the safe answer.
var secRootShared = func() bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return true
	}
	defer f.Close()
	return secRootSharedIn(f)
}

// secRootSharedIn reads a mountinfo table: the last mount on / is the
// one in effect, and "shared:N" among its optional fields (between the
// options and the "-") makes it shared. No mount on / counts as shared.
func secRootSharedIn(r io.Reader) bool {
	shared, found := false, false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		// id parent major:minor root mountpoint options [optional...] - fstype ...
		fields := strings.Fields(sc.Text())
		if len(fields) < 7 || fields[4] != "/" {
			continue
		}
		found, shared = true, false
		for _, opt := range fields[6:] {
			if opt == "-" {
				break
			}
			if strings.HasPrefix(opt, "shared:") {
				shared = true
			}
		}
	}
	return !found || shared
}

// --- Paths ---

// secBindPaths maps BindPaths= as the BindReadOnlyPaths= case maps its
// list ("[-]src[:dst[:rbind|norbind]]" to slinit's "src:dst"). Neither is
// in specifierKeys, so the specifiers are expanded here.
//
// slinit's bind-paths has no optional form: a source that is missing
// when the service starts fails the start. So whether a "-" source is
// there is decided now, when the unit is converted (or loaded, for a
// unit slinit reads directly), and the notes say so.
func secBindPaths(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v, warns := expandSpecifiers(cfg, key, val)
	entries, w := systemdBindPaths(key, v)
	for i := range w {
		if strings.Contains(w[i].Msg, "does not exist") {
			w[i].Msg += "; decided when the unit was converted, since slinit's bind-paths has no optional form " +
				"(convert or reload the unit again once the path exists)"
		}
	}
	warns = append(warns, w...)
	for _, e := range entries {
		src, _, _ := strings.Cut(e, ":")
		if secBindOptional(v, src) {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s: -%s exists now and is bound; slinit's "+
				"bind-paths has no optional form, so the start fails if it is gone by then, where systemd skips it", key, src)})
		}
	}
	cfg.setMappedList("bind-paths", v, entries)
	return warns
}

// secBindOptional reports whether src is given with "-" in the
// BindPaths= value v.
func secBindOptional(v, src string) bool {
	for _, word := range strings.Fields(v) {
		first, _, _ := strings.Cut(trimQuotes(word), ":")
		if strings.HasPrefix(first, "-") && strings.TrimLeft(first, "-") == src {
			return true
		}
	}
	return false
}

// secPathAlias maps a deprecated *Directories= through the case of the
// name that replaced it, naming the directive the unit wrote in the
// warnings.
func secPathAlias(current string) func(*SystemdConfig, string, string) []SystemdWarning {
	return func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		v, warns := expandSpecifiers(cfg, key, val)
		var pre, post int
		for _, w := range applyServiceKey(cfg, current, v, &pre, &post) {
			w.Msg = strings.ReplaceAll(w.Msg, current, key)
			warns = append(warns, w)
		}
		return warns
	}
}

// --- Per-service directories ---

// secDirMode maps *DirectoryMode=. systemd takes up to 07777
// (parse_mode); slinit's mode is the permission bits only. The setuid
// and setgid bits are dropped with a warning, which never widens access.
// A sticky bit is refused when group or others may write: without it
// they could remove each other's files, so the directory keeps slinit's
// 0755 instead.
func secDirMode(v string) (string, []SystemdWarning, error) {
	m, err := strconv.ParseUint(v, 8, 32)
	if err != nil || m > 0o7777 {
		return "", nil, fmt.Errorf("%q is no octal mode", v)
	}
	perm := m & 0o777
	if m&0o1000 != 0 && perm&0o022 != 0 {
		return "", nil, fmt.Errorf("slinit's mode cannot carry the sticky bit of %04o; the directory keeps 0755", m)
	}
	var warns []SystemdWarning
	if m > 0o777 {
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("mode %04o: slinit sets the permission bits only; "+
			"the directory gets %04o", m, perm)})
	}
	return fmt.Sprintf("%04o", perm), warns, nil
}

// secPreserve maps RuntimeDirectoryPreserve=: a boolean or restart in
// both (systemd's lookup also takes the boolean spellings).
func secPreserve(v string) (string, []SystemdWarning, error) {
	if strings.ToLower(v) == "restart" {
		return "restart", nil, nil
	}
	on, err := secBool(v)
	if err != nil {
		return "", nil, fmt.Errorf("expected yes, no or restart")
	}
	if on {
		return "yes", nil, nil
	}
	return "no", nil, nil
}

// --- Identity and process attributes ---

// secFinish decides what the security lines recorded in secState.
func secFinish(cfg *SystemdConfig) []SystemdWarning {
	s := secStateOf(cfg)
	warns := secFinishNamespaces(cfg, s)
	warns = append(warns, secFinishDynamicUser(cfg, s)...)
	warns = append(warns, secFinishLSM(cfg, s)...)
	if strings.TrimSpace(cfg.chroot) != "" {
		if v, ok := cfg.hasMapped("bind-paths"); ok {
			// slinit-runner makes the bind mounts on host paths
			// before it enters the chroot, and the loader refuses the
			// pair. A bind mount gives the service access rather
			// than taking it away, so leaving it out is a WARN.
			cfg.dropMapped("bind-paths")
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("BindPaths= with RootDirectory=%s: slinit cannot "+
				"make bind mounts for a chrooted service (they would land on the host, not in the jail), so %s is "+
				"not bound; make the bind mounts inside the jail on the host instead", cfg.chroot, v)})
		}
		if _, ok := cfg.hasMapped("private-devices"); ok {
			// The same: slinit-runner builds the private /dev on the
			// host's before the chroot, and the loader refuses the pair.
			// The service sees the jail's /dev, which the jail decides.
			cfg.dropMapped("private-devices")
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("PrivateDevices= with RootDirectory=%s: slinit "+
				"cannot build a private /dev inside a chroot, so the service sees the jail's own /dev; keep only "+
				"the pseudo devices there", cfg.chroot)})
		}
	}
	return warns
}

// secFinishDynamicUser maps DynamicUser=. Besides the transient UID,
// systemd enforces a sandbox on such a service so it cannot leave files
// or IPC behind for the next owner of the UID (unit_patch_contexts):
// RemoveIPC=, NoNewPrivileges= and RestrictSUIDSGID= forced on,
// PrivateTmp= on, ProtectHome=read-only unless set, ProtectSystem=strict.
// slinit's dynamic-user is the UID alone, so the rest is spelled out,
// forced over whatever the unit's own lines say, before or after
// DynamicUser=, as unit_patch_contexts runs after the whole unit is read.
//
// ProtectSystem=strict is not: slinit's protect-system = strict leaves
// the service no write access to its own state-, cache- and
// logs-directory, which systemd keeps writable, so it would break most
// such services. Said with a WARN instead.
func secFinishDynamicUser(cfg *SystemdConfig, s *secState) []SystemdWarning {
	warns := s.dyn.settle(cfg)
	if !s.dyn.on {
		return warns
	}
	cfg.setMapped("dynamic-user", "yes")
	secForce(cfg, "remove-ipc", "yes")
	secForce(cfg, "restrict-suidsgid", "yes")
	cfg.noNewPrivs = true
	warns = append(warns, SystemdWarning{"NOTE", "DynamicUser=yes -> dynamic-user = yes, with remove-ipc, restrict-suidsgid, " +
		"no-new-privs, private-tmp and protect-home = read-only, as systemd implies. If User= names an existing " +
		"user, systemd runs the service as that user; slinit still allocates a transient UID"})
	if strings.TrimSpace(cfg.chroot) != "" {
		// slinit refuses a chroot together with the filesystem
		// sandbox; the unit would not load at all. What the unit's
		// own PrivateTmp= and ProtectHome= lines say is left alone.
		return append(warns, SystemdWarning{"WARN", "DynamicUser=yes with RootDirectory=: slinit cannot combine chroot " +
			"with private-tmp or protect-home, so the private /tmp and read-only /home systemd implies are not applied"})
	}
	secForce(cfg, "private-tmp", "yes")
	h, _ := secMappedValue(cfg, "protect-home")
	if off, err := secBool(h); h == "" || (err == nil && !off) {
		secForce(cfg, "protect-home", "read-only")
	}
	return append(warns, SystemdWarning{"WARN", "DynamicUser=yes implies ProtectSystem=strict in systemd; not applied, " +
		"because slinit's protect-system = strict leaves the service no write access to its own state-, cache- and " +
		"logs-directory. The service can write wherever its transient UID may"})
}

// secRemoveIPC maps RemoveIPC=. systemd removes the user's IPC objects
// when the last unit running as that user stops; slinit's remove-ipc
// sweeps when this service stops, whatever else runs as the same user.
// A DynamicUser=yes forces it on in secFinishDynamicUser, whatever this
// says.
func secRemoveIPC(cfg *SystemdConfig, key, val string) []SystemdWarning {
	on, err := secBool(secValue(val))
	if err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}}
	}
	if !on {
		cfg.dropMapped("remove-ipc")
		return nil
	}
	cfg.setMapped("remove-ipc", "yes")
	return []SystemdWarning{{"NOTE", "RemoveIPC=yes -> remove-ipc = yes; slinit removes the user's IPC objects when " +
		"this service stops, also while other services of the same user still run (systemd waits for the last)"}}
}

// secSecureBitNames are the names systemd's secure_bits_from_string
// knows; slinit's securebits takes the same ones.
var secSecureBitNames = map[string]bool{
	"keep-caps": true, "keep-caps-locked": true,
	"no-setuid-fixup": true, "no-setuid-fixup-locked": true,
	"noroot": true, "noroot-locked": true,
	"no-cap-ambient-raise": true, "no-cap-ambient-raise-locked": true,
	"exec-restrict-file": true, "exec-restrict-file-locked": true,
	"exec-deny-interactive": true, "exec-deny-interactive-locked": true,
}

// secSecureBits maps SecureBits=. A scalar in systemd: each assignment
// replaces the last (DEFINE_CONFIG_PARSE), and a name it does not know
// is skipped without a word, which the mapping does too, with one.
func secSecureBits(cfg *SystemdConfig, key, val string) []SystemdWarning {
	var keep, unknown []string
	for _, w := range strings.Fields(secValue(val)) {
		if secSecureBitNames[w] {
			keep = append(keep, w)
		} else {
			unknown = append(unknown, w)
		}
	}
	var warns []SystemdWarning
	if len(unknown) > 0 {
		warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("SecureBits=: %s unknown, ignored as systemd ignores it",
			strings.Join(unknown, " "))})
	}
	if len(keep) == 0 {
		cfg.dropMapped("securebits")
		return warns
	}
	return append(warns, cfg.applyRule("Service", key, strings.Join(keep, " "),
		sdRule{key: "securebits", class: sdConfine})...)
}

// secPersonality maps Personality=. systemd takes an architecture name
// and accepts only the native one (PER_LINUX) and its secondary
// (PER_LINUX32). slinit's names choose the same two, so those pass;
// anything else systemd would refuse too, or slinit has no name for.
func secPersonality(v string) (string, []SystemdWarning, error) {
	for _, p := range secPersonalities[runtime.GOARCH] {
		if v == p {
			return v, nil, nil
		}
	}
	return "", nil, fmt.Errorf("not a personality slinit can set on this %s host", runtime.GOARCH)
}

// secPersonalities are, per host, systemd's native and secondary
// architecture names that slinit's personality also knows.
var secPersonalities = map[string][]string{
	"amd64": {"x86-64", "x86"},
	"386":   {"x86"},
	"arm64": {"arm64", "arm"},
	"arm":   {"arm"},
}

// secKeyringMode: slinit sets up no session keyring, which is
// KeyringMode=inherit. private and shared give the service a keyring of
// its own; without one it shares the keyring it inherits with every
// other service of the same user. (systemd's default for a system
// service is private, so a unit that does not say is affected too.)
func secKeyringMode(cfg *SystemdConfig, key, val string) []SystemdWarning {
	switch v := secValue(val); v {
	case "", "inherit":
		return nil
	case "private", "shared":
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s has no slinit equivalent: slinit allocates no "+
			"session keyring, so the service shares the one it inherits with every service of the same user", key, val)}}
	default:
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: expected inherit, private or shared", key, val)}}
	}
}

// secLoginEnvironment maps SetLoginEnvironment=. yes sets $HOME,
// $LOGNAME and $SHELL even for root, which slinit's export-passwd-vars
// does from run-as (or slinit's own user). no matches slinit's default.
// Empty is systemd's "auto" (set them when User= is given), which
// slinit does not do; the User= mapping is where that belongs.
func secLoginEnvironment(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := secValue(val)
	on, err := secBool(v)
	if v == "" || (err == nil && !on) {
		if o, _ := secMappedValue(cfg, "load-options"); o == "export-passwd-vars" {
			cfg.dropMapped("load-options")
		}
		return nil
	}
	if err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}}
	}
	cfg.setMapped("load-options", "export-passwd-vars")
	return []SystemdWarning{{"NOTE", "SetLoginEnvironment=yes -> load-options = export-passwd-vars, " +
		"which also exports USER, UID and GID"}}
}

// secRootDirectoryStartOnly: slinit enters a chroot for the main
// command only (ProcessService passes Chroot to it alone), so its
// pre-start, post-start and stop commands always run on the host's
// filesystem, which is what RootDirectoryStartOnly=yes asks for.
func secRootDirectoryStartOnly(cfg *SystemdConfig, key, val string) []SystemdWarning {
	on, err := secBool(secValue(val))
	if err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}}
	}
	if on {
		return nil
	}
	return []SystemdWarning{{"NOTE", fmt.Sprintf("%s=%s: slinit runs ExecStartPre=, ExecStartPost= and ExecStop= "+
		"outside RootDirectory= whatever this says, as with RootDirectoryStartOnly=yes", key, val)}}
}
