package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// RenderShow returns a systemd-`show`-style key=value dump of the
// service. Each field lives on its own line; keys sort deterministically
// within each cluster; unset optional fields are omitted. The output is
// designed to be scripting-friendly (`awk -F=`, `grep '^Key='`) — human-
// oriented rendering lives in `slinitctl status`.
//
// Fields are grouped semantically (identity → state → timestamps →
// restart → kill → cgroup → security → deps → exec) rather than
// alphabetically so a naked `slinitctl show` reads top-to-bottom like
// a systemd unit dump.
func RenderShow(s Service) string {
	sr := s.Record()
	var b strings.Builder
	p := func(k, v string) { b.WriteString(k); b.WriteByte('='); b.WriteString(v); b.WriteByte('\n') }
	pb := func(k string, v bool) { p(k, yesNo(v)) }

	// --- Identity ---
	p("Id", sr.serviceName)
	if sr.description != "" {
		p("Description", sr.description)
	}
	if sr.serviceDir != "" {
		p("FragmentPath", filepath.Join(sr.serviceDir, sr.serviceName))
	}
	if sr.author != "" {
		p("Author", sr.author)
	}
	if sr.version != "" {
		p("Version", sr.version)
	}
	if sr.usage != "" {
		p("Usage", sr.usage)
	}
	p("Type", sr.recordType.String())
	if sr.invocationID != "" {
		p("InvocationID", sr.invocationID)
	}
	if sr.provides != "" {
		p("Provides", sr.provides)
	}
	if len(sr.profiles) > 0 {
		p("Profiles", strings.Join(sr.profiles, " "))
	}
	if len(sr.bundleMembers) > 0 {
		p("BundleMembers", strings.Join(sr.bundleMembers, " "))
	}

	// --- State ---
	p("State", sr.state.Load().String())
	p("Target", sr.desired.Load().String())
	p("StopReason", sr.stopReason.String())
	pb("StartFailed", sr.startFailed)
	pb("MarkedActive", sr.startExplicit)
	pb("WaitingForDeps", sr.waitingForDeps)
	pb("HaveConsole", sr.haveConsole)

	// --- Timestamps ---
	if !sr.startRequestTime.IsZero() {
		p("StartRequestTimestamp", sr.startRequestTime.Format(time.RFC3339))
	}
	if !sr.startedTime.IsZero() {
		p("ActiveEnterTimestamp", sr.startedTime.Format(time.RFC3339))
	}
	if !sr.stoppedTime.IsZero() {
		p("InactiveEnterTimestamp", sr.stoppedTime.Format(time.RFC3339))
	}
	if !sr.loadModTime.IsZero() {
		p("LoadModTimestamp", sr.loadModTime.Format(time.RFC3339))
	}

	// --- Restart / lifecycle policy ---
	p("Restart", sr.autoRestart.String())
	p("RestartMode", sr.restartMode.String())
	pb("SmoothRecovery", sr.smoothRecovery)
	pb("RefuseManualStart", sr.refuseManualStart)
	pb("RefuseManualStop", sr.refuseManualStop)
	pb("StopWhenUnneeded", sr.stopWhenUnneeded)
	pb("ManualStart", sr.manualStart)
	if len(sr.normalExitCodes) > 0 {
		p("NormalExitCodes", intListStr(sr.normalExitCodes))
	}
	if len(sr.restartForceExitCodes) > 0 {
		p("RestartForceExitCodes", intListStr(sr.restartForceExitCodes))
	}
	if sr.jobTimeout > 0 {
		p("JobTimeoutUSec", strconv.FormatInt(sr.jobTimeout.Microseconds(), 10))
	}
	if sr.runtimeMax > 0 {
		p("RuntimeMaxUSec", strconv.FormatInt(sr.runtimeMax.Microseconds(), 10))
	}
	if sr.runtimeMaxExtra > 0 {
		p("RuntimeRandomizedExtraUSec", strconv.FormatInt(sr.runtimeMaxExtra.Microseconds(), 10))
	}
	p("OOMPolicy", sr.oomPolicy.String())

	// --- Kill signals ---
	p("KillMode", sr.killMode.String())
	if sr.termSignal != 0 {
		p("KillSignal", sigName(sr.termSignal))
	}
	if sr.reloadSignal != 0 {
		p("ReloadSignal", sigName(sr.reloadSignal))
	}
	if sr.restartKillSignal != 0 {
		p("RestartKillSignal", sigName(sr.restartKillSignal))
	}
	if sr.finalKillSignal != 0 {
		p("FinalKillSignal", sigName(sr.finalKillSignal))
	}
	if sr.watchdogSignal != 0 {
		p("WatchdogSignal", sigName(sr.watchdogSignal))
	}
	pb("SurviveFinalKillSignal", sr.surviveFinalKillSignal)
	p("TimeoutStopFailureMode", sr.timeoutStopFailureMode.String())

	// --- CGroup + slice ---
	if sr.cgroupPath != "" {
		p("CgroupPath", sr.cgroupPath)
	}
	if sr.slice != "" {
		p("Slice", sr.slice)
	}

	// --- Process attributes ---
	if sr.nice != nil {
		p("Nice", strconv.Itoa(*sr.nice))
	}
	if sr.oomScoreAdj != nil {
		p("OOMScoreAdjust", strconv.Itoa(*sr.oomScoreAdj))
	}
	if sr.umask != nil {
		p("UMask", fmt.Sprintf("%04o", *sr.umask))
	}
	pb("NoNewPrivileges", sr.noNewPrivs)
	if sr.ioPrioClass != 0 || sr.ioPrioLevel != 0 {
		p("IOSchedulingClass", strconv.Itoa(sr.ioPrioClass))
		p("IOSchedulingPriority", strconv.Itoa(sr.ioPrioLevel))
	}
	if len(sr.cpuAffinity) > 0 {
		p("CPUAffinity", uintListStr(sr.cpuAffinity))
	}
	if sr.schedPolicySet {
		p("CPUSchedulingPolicy", strconv.FormatUint(uint64(sr.schedPolicy), 10))
		p("CPUSchedulingPriority", strconv.FormatUint(uint64(sr.schedPriority), 10))
		pb("CPUSchedulingResetOnFork", sr.schedResetOnFork)
	}
	if sr.timerSlackNsec > 0 {
		p("TimerSlackNSec", strconv.FormatInt(sr.timerSlackNsec, 10))
	}
	if sr.coredumpFilter != "" {
		p("CoredumpFilter", sr.coredumpFilter)
	}
	if sr.personality != "" {
		p("Personality", sr.personality)
	}
	if sr.memoryTHP != "" {
		p("MemoryTHP", sr.memoryTHP)
	}
	pb("MemoryKSM", sr.memoryKSM)
	if sr.ignoreSIGPIPE != nil {
		pb("IgnoreSIGPIPE", *sr.ignoreSIGPIPE)
	}
	if sr.utmpMode != "" {
		p("UtmpMode", sr.utmpMode)
	}
	if sr.execSearchPath != "" {
		p("ExecSearchPath", sr.execSearchPath)
	}

	// --- Rlimits ---
	for _, rl := range sr.rlimits {
		key := rlimitKey(rl.Resource)
		if key == "" {
			continue
		}
		p("Limit"+key+"Soft", rlimitVal(rl.Soft))
		p("Limit"+key, rlimitVal(rl.Hard))
	}

	// --- Security cluster: Protect* / Restrict* / MemoryDenyWriteExecute ---
	pb("ProtectClock", sr.hardening.ProtectClock)
	pb("ProtectKernelTunables", sr.hardening.ProtectKernelTunables)
	pb("ProtectKernelModules", sr.hardening.ProtectKernelModules)
	pb("ProtectKernelLogs", sr.hardening.ProtectKernelLogs)
	pb("ProtectControlGroups", sr.hardening.ProtectControlGroups)
	pb("ProtectHostname", sr.hardening.ProtectHostname)
	pb("LockPersonality", sr.hardening.LockPersonality)
	pb("RestrictRealtime", sr.hardening.RestrictRealtime)
	pb("RestrictNamespaces", sr.hardening.RestrictNamespaces)
	pb("RestrictSUIDSGID", sr.hardening.RestrictSUIDSGID)
	pb("RestrictFileSystems", sr.hardening.RestrictFileSystems)
	pb("MemoryDenyWriteExecute", sr.hardening.MemoryDenyWriteExecute)
	if sr.hardening.RestrictAFEnabled {
		p("RestrictAddressFamilies", strings.Join(sr.hardening.RestrictAddressFamilies, " "))
	}

	// --- Filesystem sandbox ---
	if sr.sandbox.PrivateTmp {
		pb("PrivateTmp", true)
	}
	if sr.sandbox.ProtectSystem != "" {
		p("ProtectSystem", sr.sandbox.ProtectSystem)
	}
	if sr.sandbox.ProtectHome != "" {
		p("ProtectHome", sr.sandbox.ProtectHome)
	}
	if sr.sandbox.ProtectProc != "" {
		p("ProtectProc", sr.sandbox.ProtectProc)
	}
	if sr.sandbox.ProcSubset != "" {
		p("ProcSubset", sr.sandbox.ProcSubset)
	}
	if len(sr.sandbox.ReadOnlyPaths) > 0 {
		p("ReadOnlyPaths", strings.Join(sr.sandbox.ReadOnlyPaths, " "))
	}
	if len(sr.sandbox.ReadWritePaths) > 0 {
		p("ReadWritePaths", strings.Join(sr.sandbox.ReadWritePaths, " "))
	}
	if len(sr.sandbox.InaccessiblePaths) > 0 {
		p("InaccessiblePaths", strings.Join(sr.sandbox.InaccessiblePaths, " "))
	}
	if len(sr.sandbox.BindPaths) > 0 {
		p("BindPaths", strings.Join(sr.sandbox.BindPaths, " "))
	}
	if len(sr.sandbox.BindReadOnlyPaths) > 0 {
		p("BindReadOnlyPaths", strings.Join(sr.sandbox.BindReadOnlyPaths, " "))
	}
	if len(sr.sandbox.TemporaryFileSystem) > 0 {
		p("TemporaryFileSystem", strings.Join(sr.sandbox.TemporaryFileSystem, " "))
	}

	// --- Seccomp ---
	if sr.seccomp.Active() {
		if len(sr.seccomp.Filter) > 0 {
			p("SystemCallFilter", strings.Join(sr.seccomp.Filter, " "))
		}
		if len(sr.seccomp.Architectures) > 0 {
			p("SystemCallArchitectures", strings.Join(sr.seccomp.Architectures, " "))
		}
		if sr.seccomp.ErrorAction != "" {
			p("SystemCallErrorAction", sr.seccomp.ErrorAction)
		}
		if len(sr.seccomp.LogFilter) > 0 {
			p("SystemCallLog", strings.Join(sr.seccomp.LogFilter, " "))
		}
	}

	// --- LSM ---
	if sr.selinuxContext != "" {
		p("SELinuxContext", sr.selinuxContext)
	}
	if sr.smackProcessLabel != "" {
		p("SMACKProcessLabel", sr.smackProcessLabel)
	}
	if sr.appArmorLoad != "" {
		p("AppArmorLoad", sr.appArmorLoad)
	}
	if sr.appArmorSwitch != "" {
		p("AppArmorProfile", sr.appArmorSwitch)
	}

	// --- Notify / MainPID hints ---
	if sr.notifyAccessSet {
		p("NotifyAccess", sr.notifyAccess.String())
	}
	pb("GuessMainPID", sr.guessMainPID)

	// --- Actions on lifecycle transitions ---
	if sr.failureAction != ActionNone {
		p("FailureAction", sr.failureAction.String())
	}
	if sr.successAction != ActionNone {
		p("SuccessAction", sr.successAction.String())
	}
	if sr.startLimitAction != ActionNone {
		p("StartLimitAction", sr.startLimitAction.String())
	}
	if sr.rebootArgument != "" {
		p("RebootArgument", sr.rebootArgument)
	}

	// --- Bucket B / D catch-all ---
	pb("DynamicUser", sr.dynamicUser)
	pb("RemoveIPC", sr.removeIPC)
	if sr.standardInputSet {
		p("StandardInputData", fmt.Sprintf("%d bytes", len(sr.standardInput)))
	}

	// --- Namespaces decoded from cloneflags ---
	// Cloneflags is set at load time so any Private*= directive that
	// asked for a namespace is reflected here. Matches systemd's per-
	// namespace booleans.
	if sr.cloneflags != 0 {
		pb("PrivateMounts", sr.cloneflags&uintptr(syscall.CLONE_NEWNS) != 0)
		pb("PrivateUsers", sr.cloneflags&uintptr(syscall.CLONE_NEWUSER) != 0)
		pb("PrivatePIDs", sr.cloneflags&uintptr(syscall.CLONE_NEWPID) != 0)
		pb("PrivateIPC", sr.cloneflags&uintptr(syscall.CLONE_NEWIPC) != 0)
		pb("PrivateNetwork", sr.cloneflags&uintptr(syscall.CLONE_NEWNET) != 0)
	}

	// --- CPUset + NUMA ---
	if sr.cpusetPartition != "" {
		p("CPUSetPartition", sr.cpusetPartition)
	}
	if sr.startupAllowedCPUs != "" {
		p("StartupAllowedCPUs", sr.startupAllowedCPUs)
	}
	if sr.startupAllowedMemoryNodes != "" {
		p("StartupAllowedMemoryNodes", sr.startupAllowedMemoryNodes)
	}
	if sr.steadyAllowedCPUs != "" {
		p("AllowedCPUs", sr.steadyAllowedCPUs)
	}
	if sr.steadyAllowedMemoryNodes != "" {
		p("AllowedMemoryNodes", sr.steadyAllowedMemoryNodes)
	}
	if sr.numaMempolicySet {
		p("NUMAPolicy", strconv.FormatUint(uint64(sr.numaMempolicy), 10))
		if len(sr.numaNodes) > 0 {
			p("NUMAMask", uintListStr(sr.numaNodes))
		}
	}

	// --- Auto-managed directories (RuntimeDirectory=, StateDirectory=, ...) ---
	// systemd's parlance splits these into 5 top-level bases, and slinit
	// tracks each entry with a Volatile flag that maps to the runtime
	// bucket. Render every ServiceDir entry with its base + mode.
	for _, sd := range sr.serviceDirs {
		key := "ManagedDirectory"
		if sd.Volatile {
			key = "RuntimeDirectory"
		}
		p(key, fmt.Sprintf("%s (mode=%#o)", sd.Path, sd.Mode))
	}
	if len(sr.serviceDirs) > 0 {
		switch sr.runtimeDirPreserve {
		case 0:
			p("RuntimeDirectoryPreserve", "no")
		case 1:
			p("RuntimeDirectoryPreserve", "yes")
		case 2:
			p("RuntimeDirectoryPreserve", "restart")
		}
	}
	if sr.cacheDirectoryQuota > 0 {
		p("CacheDirectoryQuota", strconv.FormatInt(sr.cacheDirectoryQuota, 10))
	}
	if sr.logsDirectoryQuota > 0 {
		p("LogsDirectoryQuota", strconv.FormatInt(sr.logsDirectoryQuota, 10))
	}
	if sr.stateDirectoryQuota > 0 {
		p("StateDirectoryQuota", strconv.FormatInt(sr.stateDirectoryQuota, 10))
	}
	pb("CacheDirectoryAccounting", sr.cacheDirectoryAccounting)
	pb("LogsDirectoryAccounting", sr.logsDirectoryAccounting)
	pb("StateDirectoryAccounting", sr.stateDirectoryAccounting)

	// --- TTY cluster ---
	if sr.ttyPath != "" {
		p("TTYPath", sr.ttyPath)
		if sr.ttyColumns > 0 {
			p("TTYColumns", strconv.FormatUint(uint64(sr.ttyColumns), 10))
		}
		if sr.ttyRows > 0 {
			p("TTYRows", strconv.FormatUint(uint64(sr.ttyRows), 10))
		}
		pb("TTYReset", sr.ttyReset)
		pb("TTYVHangup", sr.ttyVHangup)
		pb("TTYVTDisallocate", sr.ttyVTDisallocate)
	}

	// --- Capabilities ---
	if len(sr.ambientCaps) > 0 {
		names := make([]string, 0, len(sr.ambientCaps))
		for _, c := range sr.ambientCaps {
			names = append(names, capName(int(c)))
		}
		sort.Strings(names)
		p("AmbientCapabilities", strings.Join(names, " "))
	}
	if len(sr.boundingCaps) > 0 {
		names := make([]string, 0, len(sr.boundingCaps))
		for _, c := range sr.boundingCaps {
			names = append(names, capName(int(c)))
		}
		sort.Strings(names)
		p("CapabilityBoundingSet", strings.Join(names, " "))
	}
	if sr.securebits != 0 {
		p("SecureBits", fmt.Sprintf("%#x", sr.securebits))
	}

	// --- Realtime scheduling extras (period/runtime/deadline are ns) ---
	if sr.schedPolicySet {
		if sr.schedRuntime > 0 {
			p("CPUSchedulingRuntimeNSec", strconv.FormatUint(sr.schedRuntime, 10))
		}
		if sr.schedDeadline > 0 {
			p("CPUSchedulingDeadlineNSec", strconv.FormatUint(sr.schedDeadline, 10))
		}
		if sr.schedPeriod > 0 {
			p("CPUSchedulingPeriodNSec", strconv.FormatUint(sr.schedPeriod, 10))
		}
	}
	if sr.mlockallFlags != 0 {
		p("MLockAll", fmt.Sprintf("%#x", sr.mlockallFlags))
	}

	// --- Path-based activation ---
	if sr.startOnPath != "" {
		p("StartOnPath", sr.startOnPath)
		if sr.startOnPathTrigger != 0 {
			p("StartOnPathTrigger", strconv.Itoa(sr.startOnPathTrigger))
		}
	}

	// --- Conditions / assertions ---
	if len(sr.predicates) > 0 {
		names := make([]string, 0, len(sr.predicates))
		for _, pr := range sr.predicates {
			names = append(names, pr.String())
		}
		sort.Strings(names)
		p("Conditions", strings.Join(names, " "))
	}

	// --- Provides + alias ---
	if sr.enableVia != "" {
		p("EnableVia", sr.enableVia)
	}
	if sr.chainTo != "" {
		p("ChainTo", sr.chainTo)
	}
	if sr.socketPath != "" {
		p("SocketPath", sr.socketPath)
	}
	if len(sr.socketPaths) > 0 {
		p("SocketPaths", strings.Join(sr.socketPaths, " "))
	}
	if sr.inittabID != "" {
		p("InittabID", sr.inittabID)
	}
	if sr.inittabLine != "" {
		p("InittabLine", sr.inittabLine)
	}

	// --- Environment (per-service extraEnv only; global env is on the set) ---
	// Via GetAllEnv, not sr.extraEnv directly: `slinitctl show` runs on a
	// control-connection goroutine and would otherwise range over the map
	// while a concurrent setenv writes it. See envMu on ServiceRecord.
	if extraEnv := sr.GetAllEnv(); len(extraEnv) > 0 {
		pairs := make([]string, 0, len(extraEnv))
		for k, v := range extraEnv {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
		}
		sort.Strings(pairs)
		p("Environment", strings.Join(pairs, " "))
	}
	if len(sr.passEnvironment) > 0 {
		p("PassEnvironment", strings.Join(sr.passEnvironment, " "))
	}
	if len(sr.unsetEnvironment) > 0 {
		p("UnsetEnvironment", strings.Join(sr.unsetEnvironment, " "))
	}

	// --- Dependencies ---
	if len(sr.dependsOn) > 0 {
		names := make([]string, 0, len(sr.dependsOn))
		for _, d := range sr.dependsOn {
			names = append(names, fmt.Sprintf("%s(%s)", d.To.Record().Name(), d.DepType.String()))
		}
		sort.Strings(names)
		p("DependsOn", strings.Join(names, " "))
	}
	if len(sr.dependents) > 0 {
		names := make([]string, 0, len(sr.dependents))
		for _, d := range sr.dependents {
			names = append(names, fmt.Sprintf("%s(%s)", d.From.Record().Name(), d.DepType.String()))
		}
		sort.Strings(names)
		p("Dependents", strings.Join(names, " "))
	}

	// --- Type-specific exec lines + credentials + working dir ---
	switch ss := s.(type) {
	case *ProcessService:
		if len(ss.command) > 0 {
			p("ExecStart", strings.Join(ss.command, " "))
		}
		if len(ss.stopCommand) > 0 {
			p("ExecStop", strings.Join(ss.stopCommand, " "))
		}
		if len(ss.preStartCommand) > 0 {
			p("ExecStartPre", strings.Join(ss.preStartCommand, " "))
		}
		if len(ss.postStartCommand) > 0 {
			p("ExecStartPost", strings.Join(ss.postStartCommand, " "))
		}
		if len(ss.finishCommand) > 0 {
			p("ExecStopPost", strings.Join(ss.finishCommand, " "))
		}
		if len(ss.readyCheckCommand) > 0 {
			p("ExecReadyCheck", strings.Join(ss.readyCheckCommand, " "))
		}
		if ss.envDir != "" {
			p("EnvironmentDirectory", ss.envDir)
		}
		if ss.workingDir != "" {
			p("WorkingDirectory", ss.workingDir)
		}
		if ss.runAsUID != 0 {
			p("User", strconv.FormatUint(uint64(ss.runAsUID), 10))
		}
		if ss.runAsGID != 0 {
			p("Group", strconv.FormatUint(uint64(ss.runAsGID), 10))
		}
		if ss.argv0 != "" {
			p("Argv0", ss.argv0)
		}
	case *ScriptedService:
		if len(ss.startCommand) > 0 {
			p("ExecStart", strings.Join(ss.startCommand, " "))
		}
		if len(ss.stopCommand) > 0 {
			p("ExecStop", strings.Join(ss.stopCommand, " "))
		}
		if ss.runAsUID != 0 {
			p("User", strconv.FormatUint(uint64(ss.runAsUID), 10))
		}
		if ss.runAsGID != 0 {
			p("Group", strconv.FormatUint(uint64(ss.runAsGID), 10))
		}
	case *BGProcessService:
		if len(ss.command) > 0 {
			p("ExecStart", strings.Join(ss.command, " "))
		}
		if len(ss.stopCommand) > 0 {
			p("ExecStop", strings.Join(ss.stopCommand, " "))
		}
		if ss.pidFile != "" {
			p("PIDFile", ss.pidFile)
		}
		if ss.runAsUID != 0 {
			p("User", strconv.FormatUint(uint64(ss.runAsUID), 10))
		}
		if ss.runAsGID != 0 {
			p("Group", strconv.FormatUint(uint64(ss.runAsGID), 10))
		}
		if ss.restartDelay > 0 {
			p("RestartUSec", strconv.FormatInt(ss.restartDelay.Microseconds(), 10))
		}
	}

	return b.String()
}

// capName maps a Linux capability number to its CAP_* symbolic name.
// The list is exhaustive for kernels up to 6.x (last cap added was
// CAP_CHECKPOINT_RESTORE = 40); anything beyond falls through to a
// numeric form so the scripting shape stays stable.
func capName(c int) string {
	names := []string{
		"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH",
		"CAP_FOWNER", "CAP_FSETID", "CAP_KILL", "CAP_SETGID",
		"CAP_SETUID", "CAP_SETPCAP", "CAP_LINUX_IMMUTABLE",
		"CAP_NET_BIND_SERVICE", "CAP_NET_BROADCAST", "CAP_NET_ADMIN",
		"CAP_NET_RAW", "CAP_IPC_LOCK", "CAP_IPC_OWNER",
		"CAP_SYS_MODULE", "CAP_SYS_RAWIO", "CAP_SYS_CHROOT",
		"CAP_SYS_PTRACE", "CAP_SYS_PACCT", "CAP_SYS_ADMIN",
		"CAP_SYS_BOOT", "CAP_SYS_NICE", "CAP_SYS_RESOURCE",
		"CAP_SYS_TIME", "CAP_SYS_TTY_CONFIG", "CAP_MKNOD", "CAP_LEASE",
		"CAP_AUDIT_WRITE", "CAP_AUDIT_CONTROL", "CAP_SETFCAP",
		"CAP_MAC_OVERRIDE", "CAP_MAC_ADMIN", "CAP_SYSLOG",
		"CAP_WAKE_ALARM", "CAP_BLOCK_SUSPEND", "CAP_AUDIT_READ",
		"CAP_PERFMON", "CAP_BPF", "CAP_CHECKPOINT_RESTORE",
	}
	if c >= 0 && c < len(names) {
		return names[c]
	}
	return "cap-" + strconv.Itoa(c)
}

// yesNo renders bools in systemd's "yes"/"no" convention.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// intListStr renders []int as space-separated decimals.
func intListStr(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return strings.Join(parts, " ")
}

// uintListStr renders []uint as space-separated decimals.
func uintListStr(xs []uint) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.FormatUint(uint64(x), 10)
	}
	return strings.Join(parts, " ")
}

// sigName maps a signal number to its symbolic SIG* name so a scripter
// can `grep KillSignal=SIGKILL`. syscall.Signal.String returns the
// libc strsignal(3) description ("terminated", "hangup") which is a
// poor match for the scripting shape; hard-code the common signals
// slinit actually accepts as directive values and fall through to
// numeric for the rest.
func sigName(s syscall.Signal) string {
	switch s {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	case syscall.SIGCHLD:
		return "SIGCHLD"
	case syscall.SIGCONT:
		return "SIGCONT"
	case syscall.SIGSTOP:
		return "SIGSTOP"
	case syscall.SIGTSTP:
		return "SIGTSTP"
	case syscall.SIGIO:
		return "SIGIO"
	case syscall.SIGPWR:
		return "SIGPWR"
	case syscall.SIGSYS:
		return "SIGSYS"
	case syscall.SIGWINCH:
		return "SIGWINCH"
	}
	return strconv.Itoa(int(s))
}

// rlimitKey maps a POSIX RLIMIT_* constant to its systemd key suffix.
// Unknown resources return "" so the show output stays quiet rather
// than emitting `LimitUnknown=` lines.
func rlimitKey(res int) string {
	switch res {
	case unix.RLIMIT_CPU:
		return "CPU"
	case unix.RLIMIT_FSIZE:
		return "FSIZE"
	case unix.RLIMIT_DATA:
		return "DATA"
	case unix.RLIMIT_STACK:
		return "STACK"
	case unix.RLIMIT_CORE:
		return "CORE"
	case unix.RLIMIT_RSS:
		return "RSS"
	case unix.RLIMIT_NOFILE:
		return "NOFILE"
	case unix.RLIMIT_AS:
		return "AS"
	case unix.RLIMIT_NPROC:
		return "NPROC"
	case unix.RLIMIT_MEMLOCK:
		return "MEMLOCK"
	case unix.RLIMIT_LOCKS:
		return "LOCKS"
	case unix.RLIMIT_SIGPENDING:
		return "SIGPENDING"
	case unix.RLIMIT_MSGQUEUE:
		return "MSGQUEUE"
	case unix.RLIMIT_NICE:
		return "NICE"
	case unix.RLIMIT_RTPRIO:
		return "RTPRIO"
	case unix.RLIMIT_RTTIME:
		return "RTTIME"
	}
	return ""
}

// rlimitVal renders `RLIM_INFINITY` as "infinity" (matches systemd) and
// everything else as decimal.
func rlimitVal(v uint64) string {
	// RLIM_INFINITY has the top bit set on every arch Linux supports;
	// systemd renders it as "infinity" so scripts can string-compare.
	if v == ^uint64(0) {
		return "infinity"
	}
	return strconv.FormatUint(v, 10)
}
