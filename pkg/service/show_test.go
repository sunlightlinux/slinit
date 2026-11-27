package service

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/pkg/process"
	"golang.org/x/sys/unix"
)

// showKV parses RenderShow output into its keys, in order, and a map.
// A key printed twice fails the test: a script doing `grep '^Key='`
// would see two answers.
func showKV(t *testing.T, s Service) ([]string, map[string]string) {
	t.Helper()
	out := RenderShow(s)
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("show output does not end in a newline: %q", out)
	}
	var keys []string
	kv := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			t.Fatalf("line %q is not Key=Value", line)
		}
		if _, dup := kv[k]; dup {
			t.Fatalf("key %s printed twice:\n%s", k, out)
		}
		keys = append(keys, k)
		kv[k] = v
	}
	return keys, kv
}

func wantKV(t *testing.T, kv map[string]string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got, ok := kv[k]
		if !ok {
			t.Errorf("%s missing", k)
			continue
		}
		if got != v {
			t.Errorf("%s=%q, want %q", k, got, v)
		}
	}
}

func wantAbsent(t *testing.T, kv map[string]string, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := kv[k]; ok {
			t.Errorf("%s=%q printed for an unset field", k, v)
		}
	}
}

// The keys a bare service with nothing configured prints, in order.
// Everything else is "unset optional fields are omitted" (slinitctl(8)).
var showAlwaysKeys = []string{
	"Id", "Type",
	"State", "Target", "StopReason", "StartFailed", "MarkedActive", "WaitingForDeps", "HaveConsole",
	"Restart", "RestartMode", "SmoothRecovery",
	"RefuseManualStart", "RefuseManualStop", "StopWhenUnneeded", "ManualStart",
	"OOMPolicy",
	"KillMode", "KillSignal", "SurviveFinalKillSignal", "TimeoutStopFailureMode",
	"NoNewPrivileges", "MemoryKSM",
	"ProtectClock", "ProtectKernelTunables", "ProtectKernelModules", "ProtectKernelLogs",
	"ProtectControlGroups", "ProtectHostname", "LockPersonality", "RestrictRealtime",
	"RestrictNamespaces", "RestrictSUIDSGID", "RestrictFileSystems", "MemoryDenyWriteExecute",
	"GuessMainPID", "DynamicUser", "RemoveIPC",
	"CacheDirectoryAccounting", "LogsDirectoryAccounting", "StateDirectoryAccounting",
}

func TestRenderShowUnsetServicePrintsOnlyAlwaysKeys(t *testing.T) {
	set, _ := newTestSet()
	for _, s := range []Service{
		NewInternalService(set, "bare-internal"),
		NewTriggeredService(set, "bare-triggered"),
		NewProcessService(set, "bare-process"),
		NewBGProcessService(set, "bare-bgprocess"),
		NewScriptedService(set, "bare-scripted"),
	} {
		keys, kv := showKV(t, s)
		want := showAlwaysKeys
		switch s.(type) {
		case *BGProcessService, *ProcessService:
			// restart-delay has a non-zero default (200ms) for both.
			want = append(append([]string(nil), showAlwaysKeys...), "RestartUSec")
		}
		if strings.Join(keys, " ") != strings.Join(want, " ") {
			t.Errorf("%s: keys\n got %v\nwant %v", s.Record().Name(), keys, want)
		}
		wantKV(t, kv, map[string]string{
			"Id":             s.Record().Name(),
			"Type":           s.Record().recordType.String(),
			"State":          "STOPPED",
			"Target":         "STOPPED",
			"StartFailed":    "no",
			"HaveConsole":    "no",
			"ProtectClock":   "no",
			"DynamicUser":    "no",
			"WaitingForDeps": "no",
			"KillSignal":     "SIGTERM", // term-signal default
		})
	}
}

func TestRenderShowTypeNames(t *testing.T) {
	set, _ := newTestSet()
	for _, c := range []struct {
		s    Service
		want string
	}{
		{NewProcessService(set, "a"), "process"},
		{NewBGProcessService(set, "b"), "bgprocess"},
		{NewScriptedService(set, "c"), "scripted"},
		{NewInternalService(set, "d"), "internal"},
		{NewTriggeredService(set, "e"), "triggered"},
	} {
		_, kv := showKV(t, c.s)
		if kv["Type"] != c.want {
			t.Errorf("%s: Type=%q, want %q", c.s.Record().Name(), kv["Type"], c.want)
		}
	}
}

func TestRenderShowProcessPopulated(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "web")
	dep := NewInternalService(set, "net")
	user := NewInternalService(set, "front")
	sr := s.Record()

	ts := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	nice, oom := -5, 200
	umask := uint32(0o027)
	sigpipe := false

	sr.description = "web server"
	sr.serviceDir = "/etc/slinit.d"
	sr.state.Store(StateStarted)
	sr.desired.Store(StateStarted)
	sr.startExplicit = true
	sr.startRequestTime = ts
	sr.startedTime = ts.Add(time.Second)
	sr.stoppedTime = ts.Add(-time.Hour)
	sr.normalExitCodes = []int{0, 2}
	sr.restartForceExitCodes = []int{75}
	sr.jobTimeout = 2500 * time.Millisecond
	sr.runtimeMax = time.Minute
	sr.termSignal = syscall.SIGTERM
	sr.reloadSignal = syscall.SIGHUP
	sr.restartKillSignal = syscall.SIGINT
	sr.finalKillSignal = syscall.SIGKILL
	sr.watchdogSignal = syscall.SIGABRT
	sr.cgroupPath = "/sys/fs/cgroup/web"
	sr.delegate = true
	sr.delegateControllers = []string{"cpu", "memory"}
	sr.nice = &nice
	sr.oomScoreAdj = &oom
	sr.umask = &umask
	sr.noNewPrivs = true
	sr.ioPrioClass, sr.ioPrioLevel = 2, 4
	sr.cpuAffinity = []uint{0, 3}
	sr.ignoreSIGPIPE = &sigpipe
	sr.rlimits = []process.Rlimit{
		{Resource: unix.RLIMIT_NOFILE, Soft: 1024, Hard: ^uint64(0)},
		{Resource: unix.RLIMIT_CORE, Soft: 0, Hard: 0},
		{Resource: 99, Soft: 1, Hard: 1}, // unknown: dropped, not LimitUnknown=
	}
	sr.hardening.ProtectClock = true
	sr.hardening.RestrictAFEnabled = true
	sr.hardening.RestrictAddressFamilies = []string{"AF_UNIX", "AF_INET"}
	sr.sandbox.PrivateTmp = true
	sr.sandbox.ProtectSystem = "strict"
	sr.sandbox.ReadWritePaths = []string{"/var/lib/web", "/run/web"}
	sr.seccomp.Filter = []string{"@system-service"}
	sr.seccomp.ErrorAction = "EPERM"
	sr.cloneflags = uintptr(syscall.CLONE_NEWNS | syscall.CLONE_NEWNET)
	sr.serviceDirs = []process.ServiceDir{
		{Path: "/run/web", Mode: 0o755, Volatile: true},
		{Path: "/var/lib/web", Mode: 0o700},
	}
	sr.runtimeDirPreserve = 2
	sr.ambientCaps = []uintptr{unix.CAP_NET_BIND_SERVICE, unix.CAP_CHOWN}
	sr.boundingCaps = []uintptr{unix.CAP_CHECKPOINT_RESTORE, 41}
	sr.securebits = 0x2f
	sr.failureAction = ActionReboot
	sr.SetEnvVar("B", "2")
	sr.SetEnvVar("A", "1")
	sr.passEnvironment = []string{"TERM", "LANG"}

	sr.dependsOn = append(sr.dependsOn, NewServiceDep(s, dep, DepWaitsFor))
	user.Record().dependsOn = append(user.Record().dependsOn, NewServiceDep(user, s, DepRegular))
	sr.dependents = append(sr.dependents, user.Record().dependsOn[0])

	s.command = []string{"/usr/bin/web", "--port", "80"}
	s.stopCommand = []string{"/usr/bin/web-stop"}
	s.preStartCommand = []string{"/usr/bin/web-check"}
	s.finishCommand = []string{"/usr/bin/web-clean"}
	sr.SetPrivilegedHooks(PrivilegedHooks{Stop: true, Finish: true})
	s.envDir = "/etc/web/env.d"
	s.workingDir = "/srv/web"
	s.runAsUID, s.runAsGID = 1000, 1001
	s.argv0 = "webd"

	_, kv := showKV(t, s)
	wantKV(t, kv, map[string]string{
		"Id":                       "web",
		"Description":              "web server",
		"FragmentPath":             "/etc/slinit.d/web",
		"Type":                     "process",
		"State":                    "STARTED",
		"Target":                   "STARTED",
		"MarkedActive":             "yes",
		"StartRequestTimestamp":    "2026-11-26T10:00:00Z",
		"ActiveEnterTimestamp":     "2026-11-26T10:00:01Z",
		"InactiveEnterTimestamp":   "2026-11-26T09:00:00Z",
		"NormalExitCodes":          "0 2",
		"RestartForceExitCodes":    "75",
		"JobTimeoutUSec":           "2500000",
		"RuntimeMaxUSec":           "60000000",
		"KillSignal":               "SIGTERM",
		"ReloadSignal":             "SIGHUP",
		"RestartKillSignal":        "SIGINT",
		"FinalKillSignal":          "SIGKILL",
		"WatchdogSignal":           "SIGABRT",
		"CgroupPath":               "/sys/fs/cgroup/web",
		"Delegate":                 "cpu memory",
		"Nice":                     "-5",
		"OOMScoreAdjust":           "200",
		"UMask":                    "0027",
		"NoNewPrivileges":          "yes",
		"IOSchedulingClass":        "2",
		"IOSchedulingPriority":     "4",
		"CPUAffinity":              "0 3",
		"IgnoreSIGPIPE":            "no",
		"LimitNOFILESoft":          "1024",
		"LimitNOFILE":              "infinity",
		"LimitCORESoft":            "0",
		"LimitCORE":                "0",
		"ProtectClock":             "yes",
		"ProtectHostname":          "no",
		"RestrictAddressFamilies":  "AF_UNIX AF_INET",
		"PrivateTmp":               "yes",
		"ProtectSystem":            "strict",
		"ReadWritePaths":           "/var/lib/web /run/web",
		"SystemCallFilter":         "@system-service",
		"SystemCallErrorAction":    "EPERM",
		"PrivateMounts":            "yes",
		"PrivateNetwork":           "yes",
		"PrivateUsers":             "no",
		"PrivatePIDs":              "no",
		"PrivateIPC":               "no",
		"RuntimeDirectory":         "/run/web (mode=0755)",
		"ManagedDirectory":         "/var/lib/web (mode=0700)",
		"RuntimeDirectoryPreserve": "restart",
		"AmbientCapabilities":      "CAP_CHOWN CAP_NET_BIND_SERVICE",
		"CapabilityBoundingSet":    "CAP_CHECKPOINT_RESTORE cap-41",
		"SecureBits":               "0x2f",
		"FailureAction":            ActionReboot.String(),
		"Environment":              "A=1 B=2",
		"PassEnvironment":          "TERM LANG",
		"DependsOn":                "net(waits-for)",
		"Dependents":               "front(regular)",
		"ExecStart":                "/usr/bin/web --port 80",
		"ExecStop":                 "+/usr/bin/web-stop",
		"ExecStartPre":             "/usr/bin/web-check",
		"ExecStopPost":             "+/usr/bin/web-clean",
		"EnvironmentDirectory":     "/etc/web/env.d",
		"WorkingDirectory":         "/srv/web",
		"User":                     "1000",
		"Group":                    "1001",
		"Argv0":                    "webd",
	})
	wantAbsent(t, kv, "LimitUnknown", "LimitSoft", "Limit", "ProtectHome",
		"SystemCallArchitectures", "SuccessAction", "StartLimitAction",
		"ExecStartPost", "PIDFile", "RestartSteps", "UnsetEnvironment")
}

// Identity first and exec last, in the documented cluster order:
// identity, state, timestamps, restart, kill, cgroup, security, deps, exec.
func TestRenderShowClusterOrder(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "ord")
	sr := s.Record()
	sr.description = "d"
	sr.startedTime = time.Unix(1, 0).UTC()
	sr.termSignal = syscall.SIGTERM
	sr.cgroupPath = "/cg"
	dep := NewInternalService(set, "dep")
	sr.dependsOn = append(sr.dependsOn, NewServiceDep(s, dep, DepRegular))
	s.command = []string{"/bin/true"}

	keys, _ := showKV(t, s)
	idx := map[string]int{}
	for i, k := range keys {
		idx[k] = i
	}
	order := []string{"Id", "Description", "Type", "State", "ActiveEnterTimestamp",
		"Restart", "KillMode", "KillSignal", "CgroupPath", "ProtectClock", "DependsOn", "ExecStart"}
	for i := 1; i < len(order); i++ {
		if idx[order[i-1]] >= idx[order[i]] {
			t.Errorf("%s (line %d) is not before %s (line %d)",
				order[i-1], idx[order[i-1]], order[i], idx[order[i]])
		}
	}
}

func TestRenderShowBGProcess(t *testing.T) {
	set, _ := newTestSet()
	s := NewBGProcessService(set, "daemon")
	s.command = []string{"/usr/sbin/daemond", "-f"}
	s.stopCommand = []string{"/usr/sbin/daemond", "-k"}
	s.pidFile = "/run/daemond.pid"
	s.runAsUID, s.runAsGID = 7, 8
	s.restartDelay = 1500 * time.Millisecond
	sr := s.Record()
	sr.SetPrivilegedHooks(PrivilegedHooks{Stop: true})

	_, kv := showKV(t, s)
	wantKV(t, kv, map[string]string{
		"Type":        "bgprocess",
		"ExecStart":   "/usr/sbin/daemond -f",
		"ExecStop":    "+/usr/sbin/daemond -k",
		"PIDFile":     "/run/daemond.pid",
		"User":        "7",
		"Group":       "8",
		"RestartUSec": "1500000",
	})
	wantAbsent(t, kv, "WorkingDirectory", "Argv0", "EnvironmentDirectory")
}

func TestRenderShowScripted(t *testing.T) {
	set, _ := newTestSet()
	s := NewScriptedService(set, "mounts")
	s.startCommand = []string{"/sbin/mount", "-a"}
	s.stopCommand = []string{"/sbin/umount", "-a"}
	s.runAsUID, s.runAsGID = 0, 5

	_, kv := showKV(t, s)
	wantKV(t, kv, map[string]string{
		"Type":      "scripted",
		"ExecStart": "/sbin/mount -a",
		"ExecStop":  "/sbin/umount -a", // not privileged: no "+"
		"Group":     "5",
	})
	// uid 0 is the default (root), not printed.
	wantAbsent(t, kv, "User", "PIDFile")
}

// Internal and triggered services have no exec lines at all.
func TestRenderShowNoExecForInternalAndTriggered(t *testing.T) {
	set, _ := newTestSet()
	for _, s := range []Service{NewInternalService(set, "i"), NewTriggeredService(set, "t")} {
		_, kv := showKV(t, s)
		wantAbsent(t, kv, "ExecStart", "ExecStop", "User", "Group")
	}
}

func TestRenderShowDelegateYesWithoutControllers(t *testing.T) {
	set, _ := newTestSet()
	s := NewInternalService(set, "d")
	s.Record().delegate = true
	_, kv := showKV(t, s)
	wantKV(t, kv, map[string]string{"Delegate": "yes"})
}

func TestRenderShowRuntimeDirectoryPreserve(t *testing.T) {
	for v, want := range map[int]string{0: "no", 1: "yes", 2: "restart"} {
		set, _ := newTestSet()
		s := NewInternalService(set, "rd")
		s.Record().serviceDirs = []process.ServiceDir{{Path: "/run/x", Mode: 0o750, Volatile: true}}
		s.Record().runtimeDirPreserve = v
		_, kv := showKV(t, s)
		wantKV(t, kv, map[string]string{
			"RuntimeDirectoryPreserve": want,
			"RuntimeDirectory":         "/run/x (mode=0750)",
		})
	}
}

// sigName gives the kernel's SIG* name for the signals slinit lists, and
// the bare number for anything else, so the line stays Key=<token>.
func TestSigName(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		want string
	}{
		{syscall.SIGHUP, "SIGHUP"}, {syscall.SIGINT, "SIGINT"},
		{syscall.SIGQUIT, "SIGQUIT"}, {syscall.SIGILL, "SIGILL"},
		{syscall.SIGABRT, "SIGABRT"}, {syscall.SIGFPE, "SIGFPE"},
		{syscall.SIGKILL, "SIGKILL"}, {syscall.SIGSEGV, "SIGSEGV"},
		{syscall.SIGPIPE, "SIGPIPE"}, {syscall.SIGALRM, "SIGALRM"},
		{syscall.SIGTERM, "SIGTERM"}, {syscall.SIGUSR1, "SIGUSR1"},
		{syscall.SIGUSR2, "SIGUSR2"}, {syscall.SIGCHLD, "SIGCHLD"},
		{syscall.SIGCONT, "SIGCONT"}, {syscall.SIGSTOP, "SIGSTOP"},
		{syscall.SIGTSTP, "SIGTSTP"}, {syscall.SIGIO, "SIGIO"},
		{syscall.SIGPWR, "SIGPWR"}, {syscall.SIGSYS, "SIGSYS"},
		{syscall.SIGWINCH, "SIGWINCH"},
		// Not in the list: numeric, as the doc comment promises.
		{syscall.SIGBUS, "7"},
		{syscall.Signal(34), "34"}, // SIGRTMIN
		{syscall.Signal(64), "64"}, // SIGRTMAX
	} {
		if got := sigName(c.sig); got != c.want {
			t.Errorf("sigName(%d) = %q, want %q", int(c.sig), got, c.want)
		}
		// Every named entry must agree with the kernel's own table.
		if !strings.HasPrefix(c.want, "SIG") {
			continue
		}
		if n := unix.SignalNum(c.want); n != c.sig {
			t.Errorf("%s is signal %d to the kernel, sigName maps %d", c.want, int(n), int(c.sig))
		}
	}
}

// rlimitKey must produce systemd's Limit<X>= suffixes, one per RLIMIT_*.
func TestRlimitKey(t *testing.T) {
	want := map[int]string{
		unix.RLIMIT_CPU: "CPU", unix.RLIMIT_FSIZE: "FSIZE", unix.RLIMIT_DATA: "DATA",
		unix.RLIMIT_STACK: "STACK", unix.RLIMIT_CORE: "CORE", unix.RLIMIT_RSS: "RSS",
		unix.RLIMIT_NOFILE: "NOFILE", unix.RLIMIT_AS: "AS", unix.RLIMIT_NPROC: "NPROC",
		unix.RLIMIT_MEMLOCK: "MEMLOCK", unix.RLIMIT_LOCKS: "LOCKS",
		unix.RLIMIT_SIGPENDING: "SIGPENDING", unix.RLIMIT_MSGQUEUE: "MSGQUEUE",
		unix.RLIMIT_NICE: "NICE", unix.RLIMIT_RTPRIO: "RTPRIO", unix.RLIMIT_RTTIME: "RTTIME",
	}
	if len(want) != 16 { // RLIM_NLIMITS on Linux
		t.Fatalf("table has %d entries, want 16", len(want))
	}
	for res, key := range want {
		if got := rlimitKey(res); got != key {
			t.Errorf("rlimitKey(%d) = %q, want %q", res, got, key)
		}
	}
	for _, res := range []int{-1, 16, 99} {
		if got := rlimitKey(res); got != "" {
			t.Errorf("rlimitKey(%d) = %q, want \"\"", res, got)
		}
	}
}

func TestRlimitVal(t *testing.T) {
	for v, want := range map[uint64]string{
		0: "0", 1024: "1024", ^uint64(0): "infinity", ^uint64(0) - 1: "18446744073709551614",
	} {
		if got := rlimitVal(v); got != want {
			t.Errorf("rlimitVal(%d) = %q, want %q", v, got, want)
		}
	}
}

func TestCapName(t *testing.T) {
	for c, want := range map[int]string{
		unix.CAP_CHOWN: "CAP_CHOWN", unix.CAP_SYS_ADMIN: "CAP_SYS_ADMIN",
		unix.CAP_NET_BIND_SERVICE: "CAP_NET_BIND_SERVICE", unix.CAP_SETFCAP: "CAP_SETFCAP",
		unix.CAP_CHECKPOINT_RESTORE: "CAP_CHECKPOINT_RESTORE", 41: "cap-41", -1: "cap--1",
	} {
		if got := capName(c); got != want {
			t.Errorf("capName(%d) = %q, want %q", c, got, want)
		}
	}
}

// The rest of the optional keys: each appears with its value when its
// field is set (an unset service printing none of them is pinned by
// TestRenderShowUnsetServicePrintsOnlyAlwaysKeys).
func TestRenderShowOptionalFields(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "opt")
	sr := s.Record()
	sr.author = "ops"
	sr.version = "1.2"
	sr.usage = "serve"
	sr.invocationID = "0123abcd"
	sr.provides = "httpd"
	sr.profiles = []string{"desktop", "server"}
	sr.bundleMembers = []string{"a", "b"}
	sr.loadModTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sr.failureConditions = FailureConditions{Primary: FailOnProcessStop, Dependency: true}
	sr.runtimeMaxExtra = 3 * time.Millisecond
	sr.slice = "system.slice"
	sr.schedPolicySet = true
	sr.schedPolicy, sr.schedPriority = 1, 50
	sr.schedResetOnFork = true
	sr.schedRuntime, sr.schedDeadline, sr.schedPeriod = 10, 20, 30
	sr.timerSlackNsec = 50000
	sr.coredumpFilter = "0x33"
	sr.personality = "x86"
	sr.memoryTHP = "never"
	sr.utmpMode = "login"
	sr.execSearchPath = "/opt/bin:/usr/bin"
	sr.sandbox.ProtectHome = "read-only"
	sr.sandbox.ProtectProc = "invisible"
	sr.sandbox.ProcSubset = "pid"
	sr.sandbox.ReadOnlyPaths = []string{"/etc", "/usr"}
	sr.sandbox.InaccessiblePaths = []string{"/root"}
	sr.sandbox.BindPaths = []string{"/a:/b"}
	sr.sandbox.BindReadOnlyPaths = []string{"/c:/d"}
	sr.sandbox.TemporaryFileSystem = []string{"/tmp:size=1M"}
	sr.seccomp.IOUringFilter = []string{"a", "b"}
	sr.seccomp.Architectures = []string{"native", "x86"}
	sr.seccomp.LogFilter = []string{"@mount"}
	sr.selinuxContext = "system_u:system_r:web_t"
	sr.smackProcessLabel = "web"
	sr.appArmorLoad = "/etc/apparmor.d/web"
	sr.appArmorSwitch = "web-profile"
	sr.notifyAccessSet = true
	sr.notifyAccess = NotifyAccessAll
	sr.successAction = ActionPoweroff
	sr.startLimitAction = ActionHalt
	sr.rebootArgument = "recovery"
	sr.standardInputSet = true
	sr.standardInput = []byte("hello")
	sr.cpusetPartition = "root"
	sr.startupAllowedCPUs = "0-1"
	sr.startupAllowedMemoryNodes = "0"
	sr.steadyAllowedCPUs = "0-3"
	sr.steadyAllowedMemoryNodes = "0-1"
	sr.numaMempolicySet = true
	sr.numaMempolicy = 2
	sr.numaNodes = []uint{0, 1}
	sr.serviceDirs = []process.ServiceDir{{Path: "/var/cache/x", Mode: 0o700}}
	sr.cacheDirectoryQuota = 1024
	sr.logsDirectoryQuota = 2048
	sr.stateDirectoryQuota = 4096
	sr.ttyPath = "/dev/tty5"
	sr.ttyColumns, sr.ttyRows = 80, 25
	sr.ttyReset = true
	sr.mlockallFlags = 3
	sr.startSkipped = true
	sr.startSkipReason = "condition failed"
	sr.startOnMount = "/mnt/data"
	sr.startOnPath = "/run/flag"
	sr.startOnPathTrigger = 2
	sr.predicates = []Predicate{
		{Kind: PredPathIsDirectory, Param: "/srv"},
		{Kind: PredPathExists, Param: "/etc/x", Negate: true, IsAssert: true},
	}
	sr.enableVia = "multi-user"
	sr.chainTo = "next"
	sr.socketPath = "/run/opt.sock"
	sr.socketPaths = []string{"/run/a.sock", "/run/b.sock"}
	sr.socketReusePort = true
	sr.inittabID = "c1"
	sr.inittabLine = "c1:12345:respawn:/sbin/getty"
	sr.unsetEnvironment = []string{"DISPLAY"}
	s.postStartCommand = []string{"/bin/post"}
	sr.SetPrivilegedHooks(PrivilegedHooks{PostStart: true})
	s.readyCheckCommand = []string{"/bin/ready", "-q"}

	_, kv := showKV(t, s)
	wantKV(t, kv, map[string]string{
		"Author":                     "ops",
		"Version":                    "1.2",
		"Usage":                      "serve",
		"InvocationID":               "0123abcd",
		"Provides":                   "httpd",
		"Profiles":                   "desktop server",
		"BundleMembers":              "a b",
		"LoadModTimestamp":           "2026-01-02T03:04:05Z",
		"FailureConditions":          sr.failureConditions.String(),
		"RuntimeRandomizedExtraUSec": "3000",
		"Slice":                      "system.slice",
		"CPUSchedulingPolicy":        "1",
		"CPUSchedulingPriority":      "50",
		"CPUSchedulingResetOnFork":   "yes",
		"CPUSchedulingRuntimeNSec":   "10",
		"CPUSchedulingDeadlineNSec":  "20",
		"CPUSchedulingPeriodNSec":    "30",
		"TimerSlackNSec":             "50000",
		"CoredumpFilter":             "0x33",
		"Personality":                "x86",
		"MemoryTHP":                  "never",
		"UtmpMode":                   "login",
		"ExecSearchPath":             "/opt/bin:/usr/bin",
		"ProtectHome":                "read-only",
		"ProtectProc":                "invisible",
		"ProcSubset":                 "pid",
		"ReadOnlyPaths":              "/etc /usr",
		"InaccessiblePaths":          "/root",
		"BindPaths":                  "/a:/b",
		"BindReadOnlyPaths":          "/c:/d",
		"TemporaryFileSystem":        "/tmp:size=1M",
		"IOUringFilter":              "a; b",
		"SystemCallArchitectures":    "native x86",
		"SystemCallLog":              "@mount",
		"SELinuxContext":             "system_u:system_r:web_t",
		"SMACKProcessLabel":          "web",
		"AppArmorLoad":               "/etc/apparmor.d/web",
		"AppArmorProfile":            "web-profile",
		"NotifyAccess":               "all",
		"SuccessAction":              "poweroff",
		"StartLimitAction":           "halt",
		"RebootArgument":             "recovery",
		"StandardInputData":          "5 bytes",
		"CPUSetPartition":            "root",
		"StartupAllowedCPUs":         "0-1",
		"StartupAllowedMemoryNodes":  "0",
		"AllowedCPUs":                "0-3",
		"AllowedMemoryNodes":         "0-1",
		"NUMAPolicy":                 "2",
		"NUMAMask":                   "0 1",
		"ManagedDirectory":           "/var/cache/x (mode=0700)",
		"RuntimeDirectoryPreserve":   "no",
		"CacheDirectoryQuota":        "1024",
		"LogsDirectoryQuota":         "2048",
		"StateDirectoryQuota":        "4096",
		"TTYPath":                    "/dev/tty5",
		"TTYColumns":                 "80",
		"TTYRows":                    "25",
		"TTYReset":                   "yes",
		"TTYVHangup":                 "no",
		"TTYVTDisallocate":           "no",
		"MLockAll":                   "0x3",
		"StartSkipped":               "yes",
		"StartSkipReason":            "condition failed",
		"StartOnMount":               "/mnt/data",
		"StartOnPath":                "/run/flag",
		"StartOnPathTrigger":         "2",
		"Conditions":                 "assert-path-exists=!/etc/x condition-path-is-directory=/srv",
		"EnableVia":                  "multi-user",
		"ChainTo":                    "next",
		"SocketPath":                 "/run/opt.sock",
		"SocketPaths":                "/run/a.sock /run/b.sock",
		"SocketReusePort":            "yes",
		"InittabID":                  "c1",
		"InittabLine":                "c1:12345:respawn:/sbin/getty",
		"UnsetEnvironment":           "DISPLAY",
		"ExecStartPost":              "+/bin/post",
		"ExecReadyCheck":             "/bin/ready -q",
	})
	// SystemCallFilter only when there is a filter, even with seccomp active.
	wantAbsent(t, kv, "SystemCallFilter", "SystemCallErrorAction", "User", "Group")
}

// Each enum prints the token the config parser takes back, and an
// out-of-range value prints something a script can still match on.
func TestTypesStringRoundTrip(t *testing.T) {
	check := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for v, w := range map[ServiceState]string{StateStopped: "STOPPED", StateStarting: "STARTING",
		StateStarted: "STARTED", StateStopping: "STOPPING", 9: "ServiceState(9)"} {
		check("ServiceState", v.String(), w)
	}
	for v, w := range map[ServiceType]string{TypePlaceholder: "placeholder", TypeProcess: "process",
		TypeBGProcess: "bgprocess", TypeScripted: "scripted", TypeInternal: "internal",
		TypeTriggered: "triggered", 99: "ServiceType(99)"} {
		check("ServiceType", v.String(), w)
	}
	for v, w := range map[DependencyType]string{DepRegular: "regular", DepSoft: "soft",
		DepWaitsFor: "waits-for", DepMilestone: "milestone", DepBefore: "before",
		DepAfter: "after", DepPreparedBy: "prepared-by", 77: "DependencyType(77)"} {
		check("DependencyType", v.String(), w)
	}
	for v, w := range map[ServiceEvent]string{EventStarted: "STARTED", EventStopped: "STOPPED",
		EventFailedStart: "FAILEDSTART", EventStartCancelled: "STARTCANCELLED",
		EventStopCancelled: "STOPCANCELLED", EventPressureMemory: "PRESSURE-MEMORY",
		EventPressureCPU: "PRESSURE-CPU", EventPressureIO: "PRESSURE-IO", 50: "ServiceEvent(50)"} {
		check("ServiceEvent", v.String(), w)
	}
	for v, w := range map[ShutdownType]string{ShutdownNone: "none", ShutdownRemain: "remain",
		ShutdownHalt: "halt", ShutdownPoweroff: "poweroff", ShutdownReboot: "reboot",
		ShutdownSoftReboot: "softreboot", ShutdownKexec: "kexec", 40: "ShutdownType(40)"} {
		check("ShutdownType", v.String(), w)
	}
	for v, w := range map[StoppedReason]string{ReasonNormal: "normal",
		ReasonDepRestart: "dependency-restart", ReasonDepFailed: "dependency-failed",
		ReasonFailed: "failed", ReasonExecFailed: "exec-failed", ReasonTimedOut: "timed-out",
		ReasonTerminated: "terminated", 30: "StoppedReason(30)"} {
		check("StoppedReason", v.String(), w)
	}

	// The rest are config values: String must parse back to itself.
	for _, a := range []SystemAction{ActionNone, ActionReboot, ActionPoweroff, ActionHalt, ActionExit} {
		if back, err := ParseSystemAction(a.String()); err != nil || back != a {
			t.Errorf("SystemAction %d: %q parses to %v, %v", a, a.String(), back, err)
		}
	}
	check("SystemAction(9)", SystemAction(9).String(), "SystemAction(9)")
	for _, p := range []OOMPolicy{OOMContinue, OOMStop, OOMKill} {
		if back, err := ParseOOMPolicy(p.String()); err != nil || back != p {
			t.Errorf("OOMPolicy %d: %q parses to %v, %v", p, p.String(), back, err)
		}
	}
	check("OOMPolicy(9)", OOMPolicy(9).String(), "OOMPolicy(9)")
	for v, w := range map[AutoRestartMode]string{RestartNever: "never", RestartAlways: "always",
		RestartOnFailure: "on-failure", RestartOnSuccess: "on-success", 8: "AutoRestartMode(8)"} {
		check("AutoRestartMode", v.String(), w)
	}
	for _, m := range []TimeoutFailureMode{TimeoutFailureTerminate, TimeoutFailureAbort, TimeoutFailureKill} {
		if back, err := ParseTimeoutFailureMode(m.String()); err != nil || back != m {
			t.Errorf("TimeoutFailureMode %d: %q parses to %v, %v", m, m.String(), back, err)
		}
	}
	for _, e := range []ExitType{ExitTypeMain, ExitTypeCgroup} {
		if back, err := ParseExitType(e.String()); err != nil || back != e {
			t.Errorf("ExitType %d: %q parses to %v, %v", e, e.String(), back, err)
		}
	}
	for _, n := range []NotifyAccess{NotifyAccessMain, NotifyAccessAll, NotifyAccessExec, NotifyAccessNone} {
		if back, err := ParseNotifyAccess(n.String()); err != nil || back != n {
			t.Errorf("NotifyAccess %d: %q parses to %v, %v", n, n.String(), back, err)
		}
	}
	for _, k := range []KillMode{KillModeProcess, KillModeControlGroup, KillModeMixed, KillModeNone} {
		if back, err := ParseKillMode(k.String()); err != nil || back != k {
			t.Errorf("KillMode %d: %q parses to %v, %v", k, k.String(), back, err)
		}
	}
	for _, r := range []RestartMode{RestartModeNormal, RestartModeDirect} {
		if back, err := ParseRestartMode(r.String()); err != nil || back != r {
			t.Errorf("RestartMode %d: %q parses to %v, %v", r, r.String(), back, err)
		}
	}
}

func TestRenderShowTonightsSettings(t *testing.T) {
	set, _ := newTestSet()
	s := NewProcessService(set, "p")
	s.SetPartOf([]string{"x", "y"})
	s.SetConflicts([]string{"c"})
	s.SetOnFailure([]string{"notify@p"})
	s.SetSendSIGHUP(true)
	s.SetRestartDelay(time.Second)
	s.SetRestartBackoff(0, time.Minute)
	s.SetRestartSteps(5)
	_, kv := showKV(t, s)
	wantKV(t, kv, map[string]string{
		"PartOf":              "x y",
		"Conflicts":           "c",
		"OnFailure":           "notify@p",
		"SendSIGHUP":          "yes",
		"RestartUSec":         "1000000",
		"RestartSteps":        "5",
		"RestartMaxDelayUSec": "60000000",
	})
}
