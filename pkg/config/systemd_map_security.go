package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
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
	// --- LSM labels ---
	//
	// systemd skips all three when the LSM is not active on the host
	// (mac_*_use()), and a "-" prefix ignores a failure to apply the
	// label. slinit's runner fails the start in both cases, so the
	// mapping asks the host the question systemd asks at exec time.
	registerService("AppArmorProfile", secLSMRule("apparmor-switch", "AppArmor", &secAppArmorDir))
	registerService("SELinuxContext", secLSMRule("selinux-context", "SELinux", &secSELinuxDir))
	registerService("SmackProcessLabel", secLSMRule("smack-process-label", "SMACK", &secSmackDir))

	// --- Namespaces ---
	registerService("PrivateIPC", sdRule{custom: secNamespace("namespace-ipc", true, nil)})
	registerService("PrivatePIDs", sdRule{custom: secNamespace("namespace-pid", true, func(key, val string) []SystemdWarning {
		// systemd mounts a /proc of the new namespace (MountAPIVFS=
		// is implied); slinit's namespace-pid only clones, so /proc
		// is still the host's.
		return []SystemdWarning{{"WARN", fmt.Sprintf("%s=%s -> namespace-pid = yes; slinit mounts no /proc of the new "+
			"PID namespace, so the service still sees every host process under /proc", key, val)}}
	})})
	// PrivateMounts= and MountFlags= both ask for a mount namespace, so
	// one saying no must not undo the other: "no" leaves the setting as
	// it is (the service may end up more confined than the unit says,
	// never less).
	registerService("PrivateMounts", sdRule{custom: secNamespace("namespace-mount", false, func(key, val string) []SystemdWarning {
		return secPropagation(key, val, "slave")
	})})
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
	registerService("DynamicUser", sdRule{custom: secDynamicUser})
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

// --- LSM labels ---

// The directories slinit-runner checks before it applies each label
// (cmd/slinit-runner: changeOnExec, lsm_extra.go). Variables so a test
// can point them at a directory that is or is not there.
var (
	secAppArmorDir = "/sys/kernel/security/apparmor"
	secSELinuxDir  = "/sys/fs/selinux"
	secSmackDir    = "/sys/fs/smackfs"
)

// secLSMRule maps AppArmorProfile=, SELinuxContext= and
// SmackProcessLabel=, each "[-]label". All three apply the same way on
// both sides: apparmor-switch writes "exec <profile>" to
// /proc/self/attr/exec, which is what aa_change_onexec() does,
// selinux-context is setexeccon(), and smack-process-label writes the
// task's own label. The difference is what happens when the label
// cannot be applied:
//
//   - LSM not active on the host: systemd skips the directive without
//     a word; slinit's runner fails the start. The mapping leaves it out,
//     as systemd would, with a note.
//   - "-" with the LSM active (profile not loaded, context invalid):
//     systemd runs the service without the label; slinit fails the
//     start. Kept fail-closed, since the alternative is a service
//     running unconfined while its unit names a profile; the note says
//     so.
func secLSMRule(slinitKey, lsm string, dir *string) sdRule {
	return sdRule{custom: func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		v, warns := expandSpecifiers(cfg, key, secValue(val))
		if v == "" {
			cfg.dropMapped(slinitKey)
			return warns
		}
		label, lenient := strings.CutPrefix(v, "-")
		if _, err := os.Stat(*dir); err != nil {
			return append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s not applied: %s is not active on this host "+
				"(no %s), and systemd skips the directive then too", key, val, lsm, *dir)})
		}
		r := sdRule{key: slinitKey, class: sdConfine, conv: func(string) (string, []SystemdWarning, error) {
			if label == "" {
				return "", nil, fmt.Errorf("no label after the \"-\"")
			}
			return label, nil, nil
		}}
		warns = append(warns, cfg.applyRule("Service", key, v, r)...)
		if lenient {
			warns = append(warns, SystemdWarning{"NOTE", fmt.Sprintf("%s=%s: with \"-\" systemd runs the service "+
				"without the label when it cannot be applied; slinit's %s fails the start instead", key, val, slinitKey)})
		}
		return warns
	}}
}

// --- Namespaces ---

// secNamespace maps a boolean Private*= onto a namespace-* switch.
// dropOnNo says whether "no" may undo an earlier yes: it may when this
// directive is the only one that sets key. extra adds the warnings of a
// yes. A value that is not a boolean refuses the unit.
func secNamespace(slinitKey string, dropOnNo bool, extra func(key, val string) []SystemdWarning) func(*SystemdConfig, string, string) []SystemdWarning {
	return func(cfg *SystemdConfig, key, val string) []SystemdWarning {
		v := secValue(val)
		if v == "" {
			// For PrivateIPC= and PrivatePIDs= an invalid value
			// systemd ignores, keeping the earlier one; for the
			// tristate PrivateMounts= a reset to "auto", which asks
			// for no namespace either. Nothing to change.
			return nil
		}
		on, err := secBool(v)
		if err != nil {
			secRefuse(cfg, key, val, err)
			return nil
		}
		if !on {
			if dropOnNo {
				cfg.dropMapped(slinitKey)
			}
			return nil
		}
		cfg.setMapped(slinitKey, "yes")
		if extra != nil {
			return extra(key, val)
		}
		return nil
	}
}

// secMountFlags maps MountFlags=. slave and private each make systemd
// create a mount namespace (exec_needs_mount_namespace); shared, the
// default, does not on its own.
func secMountFlags(cfg *SystemdConfig, key, val string) []SystemdWarning {
	switch v := strings.ToLower(secValue(val)); v {
	case "", "shared":
		return nil
	case "slave", "private":
		cfg.setMapped("namespace-mount", "yes")
		return secPropagation(key, val, v)
	default:
		secRefuse(cfg, key, val, fmt.Errorf("expected shared, slave or private"))
		return nil
	}
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
func secBindPaths(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v, warns := expandSpecifiers(cfg, key, val)
	entries, w := systemdBindPaths(key, v)
	cfg.setMappedList("bind-paths", v, entries)
	return append(warns, w...)
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

// secDynamicUser maps DynamicUser=. Besides the transient UID, systemd
// enforces a sandbox on such a service so it cannot leave files or IPC
// behind for the next owner of the UID (unit_patch_contexts):
// RemoveIPC=, NoNewPrivileges= and RestrictSUIDSGID= forced on,
// PrivateTmp= on, ProtectHome=read-only unless set, ProtectSystem=strict.
// slinit's dynamic-user is the UID alone, so the rest is spelled out.
//
// ProtectSystem=strict is not: slinit's protect-system = strict leaves
// the service no write access to its own state-, cache- and
// logs-directory, which systemd keeps writable, so it would break most
// such services. Said with a WARN instead.
//
// The implied settings are written when DynamicUser= is read; a later
// ProtectHome=, PrivateTmp= or NoNewPrivileges= line in the unit
// overrides them, where systemd would keep its forced values.
func secDynamicUser(cfg *SystemdConfig, key, val string) []SystemdWarning {
	if secValue(val) == "" {
		// Not a boolean; systemd ignores it and keeps the last.
		return nil
	}
	on, err := secBool(secValue(val))
	if err != nil {
		secRefuse(cfg, key, val, err)
		return nil
	}
	if !on {
		cfg.dropMapped("dynamic-user")
		return nil
	}
	cfg.setMapped("dynamic-user", "yes")
	secForce(cfg, "remove-ipc", "yes")
	secForce(cfg, "restrict-suidsgid", "yes")
	cfg.noNewPrivs = true
	warns := []SystemdWarning{{"NOTE", "DynamicUser=yes -> dynamic-user = yes, with remove-ipc, restrict-suidsgid, " +
		"no-new-privs, private-tmp and protect-home = read-only, as systemd implies. If User= names an existing " +
		"user, systemd runs the service as that user; slinit still allocates a transient UID"}}
	if cfg.chroot != "" {
		// slinit refuses a chroot together with the filesystem
		// sandbox; the unit would not load at all.
		return append(warns, SystemdWarning{"WARN", "DynamicUser=yes with RootDirectory=: slinit cannot combine chroot " +
			"with private-tmp or protect-home, so the private /tmp and read-only /home systemd implies are not applied"})
	}
	secForce(cfg, "private-tmp", "yes")
	if h, ok := secMappedValue(cfg, "protect-home"); !ok || h == "" || h == "no" {
		secForce(cfg, "protect-home", "read-only")
	}
	return append(warns, SystemdWarning{"WARN", "DynamicUser=yes implies ProtectSystem=strict in systemd; not applied, " +
		"because slinit's protect-system = strict leaves the service no write access to its own state-, cache- and " +
		"logs-directory. The service can write wherever its transient UID may"})
}

// secRemoveIPC maps RemoveIPC=. systemd removes the user's IPC objects
// when the last unit running as that user stops; slinit's remove-ipc
// sweeps when this service stops, whatever else runs as the same user.
// "no" cannot undo a DynamicUser=yes, which forces it on in systemd.
func secRemoveIPC(cfg *SystemdConfig, key, val string) []SystemdWarning {
	on, err := secBool(secValue(val))
	if err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}}
	}
	if !on {
		if v, _ := secMappedValue(cfg, "dynamic-user"); v != "yes" {
			cfg.dropMapped("remove-ipc")
		}
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
