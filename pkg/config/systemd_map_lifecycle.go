package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The lifecycle directives of [Service]: how a service is restarted,
// stopped, killed and timed, what its exit statuses mean, and what the
// system does when it fails. slinit implements most of them under
// systemd's own names (kill-mode, restart-kill-signal, oom-policy, ...),
// but several of those settings differ from systemd in a way a unit
// cannot see. Each such difference is a NOTE from the conv below rather
// than a silent mapping, and the ones that change what the directive
// means are gaps.
//
// Some directives only have a meaning together with another one the
// unit may set before or after it, in a drop-in as well: the two exit
// status lists share normal-exit, RuntimeMaxSec= and BusName= depend
// on Type=. Their rules record what the lines say
// in lcState, and lcFinish decides once the whole unit is read, so the
// order of the lines does not change the result. The rest say what
// holds in every case (RemainAfterExit=), or are gaps (TimeoutAbortSec=).
// RestartSteps= and RestartMaxDelaySec= apply only together, and on top
// of RestartSec=.

// lcState is what the lifecycle rules saw, for lcFinish.
type lcState struct {
	// The two lists systemd keeps apart and slinit folds into
	// normal-exit: an empty assignment of one resets that one only.
	prevent, success []string
	successRaw       []string // the SuccessExitStatus= values, for the note

	runtimeMax    string // runtime-max-sec, "" for no limit
	runtimeMaxRaw string

	busName, busNameRaw string

	// RestartSteps= and RestartMaxDelaySec=, as written.
	restartSteps, restartMaxDelay string
}

func lcStateOf(cfg *SystemdConfig) *lcState {
	return sdState(cfg, "lifecycle", func() *lcState { return &lcState{} })
}

func init() {
	// --- Restart policy ---

	// slinit's normal-exit is upstart's "normal exit": exit codes and
	// signals that keep the service from being restarted, under every
	// restart= setting. That is RestartPreventExitStatus= exactly.
	registerService("RestartPreventExitStatus", sdRule{custom: lcExitList})
	// SuccessExitStatus= says the exit is a success. slinit has no such
	// list; normal-exit gives the part restart = on-failure sees, which
	// is what most units list these for (SuccessExitStatus=143 on a JVM).
	// Exit 0 is left out: a success in systemd already, as normal-exit
	// it would stop restart = yes from restarting a clean exit.
	registerService("SuccessExitStatus", sdRule{custom: lcExitList})
	// slinit's list takes exit codes only. 0 is kept: it forces a restart
	// after a clean exit in both (IsForceRestartExit takes it).
	registerService("RestartForceExitStatus", sdRule{key: "restart-force-exit-status", shape: sdList,
		conv: lcStatusList("RestartForceExitStatus", false, false)})
	registerService("RestartMode", sdRule{key: "restart-mode", conv: lcRestartMode})
	registerService("RestartRandomizedDelaySec", sdRule{key: "restart-randomized-delay",
		// systemd refuses infinity here and ignores the line (service_verify).
		conv: lcSeconds("")})

	// systemd's start rate limit, in its pre-v230 [Service] spelling.
	// Both 0 values turn the limit off on either side, and infinity is
	// a window that never ends: secondsToDuration saturates it.
	registerService("StartLimitInterval", sdRule{key: "restart-limit-interval", conv: lcSeconds("infinity")})
	registerService("StartLimitBurst", sdRule{key: "restart-limit-count", conv: lcStartLimitBurst})
	registerService("StartLimitAction", sdRule{key: "start-limit-action", conv: lcAction})
	registerService("FailureAction", sdRule{key: "failure-action", conv: lcAction})
	registerService("RebootArgument", sdRule{custom: lcRebootArgument})

	// --- Stopping and killing ---

	registerService("KillMode", sdRule{key: "kill-mode", conv: lcKillMode})
	// An empty assignment resets the signal to its default in systemd
	// (config_parse_signal), so it drops the setting. ReloadSignal= is
	// decided with ExecReload= (systemd_map_exec.go).
	registerService("FinalKillSignal", exScalar("final-kill-signal", lcSignal))
	registerService("RestartKillSignal", exScalar("restart-kill-signal", lcSignal))
	registerService("WatchdogSignal", exScalar("watchdog-signal", lcSignal))
	// slinit's survive-final-kill-signal is SendSIGKILL= inverted: it
	// turns off the final-kill-signal escalation of a stop that timed
	// out, and nothing else.
	registerService("SendSIGKILL", sdRule{key: "survive-final-kill-signal", conv: lcSendSIGKILL})
	registerService("TimeoutStartFailureMode", sdRule{key: "timeout-start-failure-mode"})
	registerService("TimeoutStopFailureMode", sdRule{key: "timeout-stop-failure-mode", conv: lcStopFailureMode})
	registerService("ExitType", sdRule{key: "exit-type", conv: lcExitType})
	registerService("OOMPolicy", sdRule{key: "oom-policy"})
	registerService("RuntimeMaxSec", sdRule{custom: lcRuntimeMaxRule})
	registerService("RuntimeRandomizedExtraSec", sdRule{key: "runtime-randomized-extra", conv: lcSeconds("infinity")})

	// --- Process setup and notification ---

	registerService("IgnoreSIGPIPE", sdRule{key: "ignore-sigpipe", conv: lcBool})
	registerService("GuessMainPID", sdRule{key: "guess-main-pid", conv: lcBool})
	registerService("SendSIGHUP", sdRule{key: "send-sighup", conv: lcBool})
	registerService("NotifyAccess", sdRule{key: "notify-access"})
	registerService("BusName", sdRule{custom: lcBusName})
	registerService("FileDescriptorStoreMax", sdRule{key: "file-descriptor-store-max", conv: lcUnsigned})
	registerService("FileDescriptorStorePreserve", sdRule{key: "file-descriptor-store-preserve", conv: lcFDStorePreserve})
	registerService("ExecCondition", sdRule{custom: lcExecCondition})
	registerService("RemainAfterExit", sdRule{custom: lcRemainAfterExit})
	// A rule, not a gap: the bespoke case for WatchdogSec= is reached
	// before the gap table, and it pointed at a setting that does not
	// take the keepalives such a service sends.
	registerService("WatchdogSec", sdRule{custom: mapWatchdogSec})

	// --- No equivalent ---

	for k, why := range map[string]string{
		"TimeoutAbortSec": "it bounds the wait after a watchdog abort, and slinit's timeout-abort-sec instead puts a SIGABRT " +
			"phase into every stop timeout; with TimeoutStopFailureMode=abort, set timeout-abort-sec by hand",
		"NonBlocking": "the socket descriptors slinit passes keep the blocking mode they were created with",
		"PermissionsStartOnly": "slinit runs ExecStartPre=, ExecStartPost= and ExecStop= with the service's run-as; " +
			"give those commands slinit's \"+\" prefix to run them as root",
		"ExecReloadPost":         "slinit's reload is a signal (reload-signal) and runs no command after it",
		"RefreshOnReload":        "slinit's reload does not refresh credentials or extension images",
		"USBFunctionDescriptors": "slinit does not set up USB FunctionFS endpoints; write the descriptors from an ExecStartPre= script",
		"USBFunctionStrings":     "slinit does not set up USB FunctionFS endpoints; write the strings from an ExecStartPre= script",
	} {
		sdServiceNoEquivalent[k] = sdGap{why: why}
	}

	// systemd parses these into nothing itself (config_parse_warn_compat,
	// DISABLED_LEGACY), or uses them only for an operation slinit lacks.
	sdServiceIgnored["BusPolicy"] = "kdbus-era; systemd ignores it since v226"
	sdServiceIgnored["SysVStartPriority"] = "SysV ordering hint; systemd ignores it"
	sdServiceIgnored["TimeoutCleanSec"] = "bounds `systemctl clean`, an operation slinit does not have"

	registerService("RestartSteps", sdRule{custom: func(cfg *SystemdConfig, _, val string) []SystemdWarning {
		lcStateOf(cfg).restartSteps = strings.TrimSpace(val)
		return nil
	}})
	registerService("RestartMaxDelaySec", sdRule{custom: func(cfg *SystemdConfig, _, val string) []SystemdWarning {
		lcStateOf(cfg).restartMaxDelay = strings.TrimSpace(val)
		return nil
	}})
	// The socket units to read with the service (systemd_socket.go).
	registerService("Sockets", sdRule{custom: func(cfg *SystemdConfig, _, val string) []SystemdWarning {
		f := strings.Fields(val)
		if len(f) == 0 {
			cfg.socketUnits = nil
		}
		for _, n := range f {
			if !strings.HasSuffix(n, ".socket") {
				return []SystemdWarning{{"WARN", "Sockets=" + n + " is not a .socket unit — ignored, as systemd does"}}
			}
			cfg.socketUnits = append(cfg.socketUnits, n)
		}
		return nil
	}})
	registerFinisher(lcFinish)
}

// lcExitList records one RestartPreventExitStatus= / SuccessExitStatus=
// assignment in its own list. The words systemd would log and skip are
// reported here, where the line is.
func lcExitList(cfg *SystemdConfig, key, val string) []SystemdWarning {
	st := lcStateOf(cfg)
	v := strings.TrimSpace(trimQuotes(val))
	if key == "SuccessExitStatus" {
		if v == "" {
			st.success, st.successRaw = nil, nil
			return nil
		}
		out, warns, _ := lcStatusList(key, true, true)(v)
		st.success = append(st.success, strings.Fields(out)...)
		st.successRaw = append(st.successRaw, v)
		return warns
	}
	if v == "" {
		st.prevent = nil
		return nil
	}
	out, warns, _ := lcStatusList(key, true, false)(v)
	st.prevent = append(st.prevent, strings.Fields(out)...)
	return warns
}

// lcRuntimeMaxRule records RuntimeMaxSec=; lcFinish decides whether it
// can be carried. A value systemd refuses keeps the earlier one.
func lcRuntimeMaxRule(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v := strings.TrimSpace(trimQuotes(val))
	out, _, err := lcRuntimeMax(v)
	if err == nil && out != "0" {
		err = validateSlinitSetting("runtime-max-sec", out)
	}
	if err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}}
	}
	st := lcStateOf(cfg)
	if out == "0" {
		st.runtimeMax, st.runtimeMaxRaw = "", "" // infinity: no limit
	} else {
		st.runtimeMax, st.runtimeMaxRaw = out, v
	}
	return nil
}

// lcFinish settles the lifecycle mappings that depend on more than one
// line of the unit.
func lcFinish(cfg *SystemdConfig) []SystemdWarning {
	st := lcStateOf(cfg)
	var warns []SystemdWarning

	// normal-exit: both lists, each status once.
	var normal []string
	seen := map[string]bool{}
	for _, w := range append(slices.Clone(st.prevent), st.success...) {
		if !seen[w] {
			seen[w] = true
			normal = append(normal, w)
		}
	}
	if len(normal) > 0 {
		v := strings.Join(normal, " ")
		if err := validateSlinitSetting("normal-exit", v); err != nil {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("normal-exit = %s ignored: %v", v, err)})
		} else {
			cfg.setMapped("normal-exit", v)
		}
	}
	if len(st.success) > 0 {
		warns = append(warns, lcSuccessWarning(cfg, strings.Join(st.successRaw, " ")))
	}

	// RestartForceExitStatus=0 on a oneshot: both restart it after every
	// run that succeeds.
	if v, ok := cfg.hasMapped("restart-force-exit-status"); ok && cfg.svcType == "scripted" &&
		slices.Contains(strings.Fields(v), "0") {
		warns = append(warns, SystemdWarning{"WARN", "RestartForceExitStatus= holds 0 on a Type=oneshot service: " +
			"it is started again after every run that succeeds, in slinit as in systemd"})
	}

	warns = append(warns, lcFinishRuntimeMax(cfg, st)...)
	warns = append(warns, lcFinishBusName(cfg, st)...)
	warns = append(warns, lcFinishRestartSteps(cfg, st)...)
	warns = append(warns, lcFinishOneshotRestart(cfg)...)
	return warns
}

// lcSuccessWarning says how normal-exit falls short of SuccessExitStatus=.
// It is a WARN where the difference changes what happens: slinit still
// counts the exit a failure, so a failure-action fires on it and a
// success-action does not; and normal-exit keeps restart = yes and
// on-success from restarting the service, which systemd does on a
// success.
func lcSuccessWarning(cfg *SystemdConfig, raw string) SystemdWarning {
	var why []string
	if a, ok := cfg.hasMapped("failure-action"); ok && a != "none" {
		why = append(why, "failure-action = "+a+" fires on these exits, where systemd counts them a success")
	}
	if a, ok := cfg.hasMapped("success-action"); ok && a != "none" {
		why = append(why, "success-action = "+a+" does not fire on these exits")
	}
	if cfg.restart == "yes" || cfg.restart == "on-success" {
		why = append(why, "restart = "+cfg.restart+" does not restart the service after these exits, where systemd does")
	}
	if len(why) > 0 {
		return SystemdWarning{"WARN", "SuccessExitStatus=" + raw + " → normal-exit, which only keeps slinit from restarting " +
			"the service on these; the exit still counts as a failure: " + strings.Join(why, "; ")}
	}
	return SystemdWarning{"NOTE", "SuccessExitStatus=" + raw + " → normal-exit, which only keeps slinit from " +
		"restarting the service on these: unlike systemd it does so under restart = yes and on-success as well, " +
		"and the exit still counts as a failure, for failure-action and for a oneshot's start"}
}

// lcFinishOneshotRestart: systemd refuses Restart=always and
// Restart=on-success on Type=oneshot (service_verify), and slinit does
// too. The other Restart= values re-run a failed oneshot in systemd;
// slinit never re-runs a scripted service's command, so the value is
// reported rather than kept with no effect.
func lcFinishOneshotRestart(cfg *SystemdConfig) []SystemdWarning {
	if cfg.svcType != "scripted" || cfg.restart == "no" {
		return nil
	}
	switch cfg.restart {
	case "yes", "on-success":
		cfg.errs = append(cfg.errs, "Restart="+cfg.restartRaw+" is not allowed for Type=oneshot; systemd refuses the unit too")
		return nil
	}
	cfg.restart = "no"
	return []SystemdWarning{{"WARN", "Restart=" + cfg.restartRaw + " not carried: slinit does not run a Type=oneshot " +
		"(scripted) service's command again when it fails"}}
}

// lcFinishRestartSteps maps RestartSteps= and RestartMaxDelaySec= onto
// restart-steps and restart-delay-cap, the same geometric curve from
// restart-delay. They need each other, as systemd's service_verify says:
// one alone is ignored with a warning. A RestartMaxDelaySec= below
// RestartSec= lowers RestartSec= to it, as systemd does, and leaves no
// curve. Without RestartSec= the curve starts at systemd's 100ms.
func lcFinishRestartSteps(cfg *SystemdConfig, st *lcState) []SystemdWarning {
	if st.restartSteps == "" && st.restartMaxDelay == "" {
		return nil
	}
	steps := 0
	if st.restartSteps != "" {
		n, err := strconv.Atoi(st.restartSteps)
		if err != nil || n < 0 || n > 3000 {
			return []SystemdWarning{{"WARN", "RestartSteps=" + st.restartSteps + " is not 0 to 3000 — ignored, as systemd does"}}
		}
		steps = n
	}
	maxSecs := ""
	if st.restartMaxDelay != "" && st.restartMaxDelay != "infinity" {
		s, ok := parseSystemdSeconds(st.restartMaxDelay, "")
		if !ok {
			return []SystemdWarning{{"WARN", "RestartMaxDelaySec=" + st.restartMaxDelay + " is no time span — ignored, as systemd does"}}
		}
		maxSecs = s
	}
	switch {
	case steps > 0 && maxSecs == "":
		return []SystemdWarning{{"WARN", "RestartSteps= without RestartMaxDelaySec= — ignored, as systemd ignores it"}}
	case steps == 0 && maxSecs != "":
		return []SystemdWarning{{"WARN", "RestartMaxDelaySec= without RestartSteps= — ignored, as systemd ignores it"}}
	case steps == 0:
		return nil
	}
	if cfg.restartDelay == "" {
		cfg.restartDelay = "0.1" // systemd's RestartSec= default
	}
	base, _ := strconv.ParseFloat(cfg.restartDelay, 64)
	limit, _ := strconv.ParseFloat(maxSecs, 64)
	if base <= 0 {
		// systemd's backoff needs RestartSec= above 0 to grow from.
		return []SystemdWarning{{"NOTE", "RestartSteps= has no effect with RestartSec=0, as in systemd"}}
	}
	if limit <= base {
		if limit < base {
			cfg.restartDelay = maxSecs
		}
		return []SystemdWarning{{"NOTE", "RestartMaxDelaySec= is not above RestartSec=: the delay stays at " +
			cfg.restartDelay + "s, as in systemd"}}
	}
	cfg.setMapped("restart-steps", strconv.Itoa(steps))
	cfg.setMapped("restart-delay-cap", maxSecs)
	return nil
}

// lcFinishRuntimeMax: systemd ignores RuntimeMaxSec= on Type=oneshot,
// whose run is bounded by TimeoutStartSec=; Type= may come after it,
// so this is decided here. Elsewhere slinit's runtime-max-sec ends the
// run as a timeout failure, so restart= applies as Restart= does.
func lcFinishRuntimeMax(cfg *SystemdConfig, st *lcState) []SystemdWarning {
	if st.runtimeMax == "" {
		return nil
	}
	if cfg.svcType == "scripted" {
		return []SystemdWarning{{"NOTE", "RuntimeMaxSec=" + st.runtimeMaxRaw + " dropped: systemd ignores it on Type=oneshot " +
			"(TimeoutStartSec= bounds the run)"}}
	}
	cfg.setMapped("runtime-max-sec", st.runtimeMax)
	return []SystemdWarning{{"NOTE", "RuntimeMaxSec= → runtime-max-sec; when it passes slinit fails the service as timed out " +
		"and restart= applies, as in systemd"}}
}

// lcWatchdogGap is why WatchdogSec= has no slinit equivalent outside a
// notify service (see systemd_map_notify.go, which maps it there).
var lcWatchdogGap = sdGap{why: "the service is not Type=notify, so it has no channel for WATCHDOG=1 keepalives; " +
	"the service runs without a watchdog"}

// lcParseBool is systemd's parse_boolean.
func lcParseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "1", "yes", "y", "true", "t", "on":
		return true, nil
	case "0", "no", "n", "false", "f", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is no boolean", v)
}

// lcBool turns a systemd boolean into slinit's, which knows no on/off,
// y/n or t/f.
func lcBool(v string) (string, []SystemdWarning, error) {
	b, err := lcParseBool(v)
	if err != nil {
		return "", nil, err
	}
	if b {
		return "yes", nil, nil
	}
	return "no", nil, nil
}

// lcSendSIGKILL: SendSIGKILL=no is survive-final-kill-signal = yes.
func lcSendSIGKILL(v string) (string, []SystemdWarning, error) {
	b, err := lcParseBool(v)
	if err != nil {
		return "", nil, err
	}
	if b {
		return "no", nil, nil
	}
	return "yes", []SystemdWarning{{"NOTE", "SendSIGKILL=no → survive-final-kill-signal = yes; after the stop timeout " +
		"slinit keeps the service stopping until its process exits, where systemd lets the unit stop and leaves the process behind"}}, nil
}

// lcSeconds converts a systemd time span to slinit's decimal seconds;
// inf is what infinity becomes, or "" when the directive refuses it.
func lcSeconds(inf string) func(string) (string, []SystemdWarning, error) {
	return func(v string) (string, []SystemdWarning, error) {
		s, ok := parseSystemdSeconds(v, inf)
		if !ok {
			return "", nil, fmt.Errorf("%q is no time span slinit can take", v)
		}
		return s, nil, nil
	}
}

// lcRuntimeMax converts RuntimeMaxSec= to runtime-max-sec, which is a
// Go duration (it requires a unit) and reads 0 as no limit.
func lcRuntimeMax(v string) (string, []SystemdWarning, error) {
	s, ok := parseSystemdSeconds(v, "0")
	if !ok {
		return "", nil, fmt.Errorf("%q is no time span", v)
	}
	if v != "infinity" {
		f, err := strconv.ParseFloat(s, 64)
		switch {
		case err != nil:
			return "", nil, err
		case f == 0:
			// systemd stops such a service the moment it is running;
			// slinit would read it as no limit.
			return "", nil, fmt.Errorf("0 stops the service as soon as it runs, and slinit reads 0 as no limit")
		case f*float64(time.Second) >= float64(maxConfigDuration):
			// Longer than a time.Duration holds (about 146 years):
			// never, which is what slinit's 0 says.
			s = "0"
		}
	}
	if s == "0" {
		// infinity, systemd's default: no limit, and it undoes an
		// earlier assignment.
		return "0", nil, nil
	}
	return s + "s", nil, nil
}

// lcUnsigned is systemd's config_parse_unsigned for a decimal value.
func lcUnsigned(v string) (string, []SystemdWarning, error) {
	n, err := strconv.ParseUint(v, 10, 31)
	if err != nil {
		return "", nil, fmt.Errorf("%q is no count", v)
	}
	return strconv.FormatUint(n, 10), nil, nil
}

func lcStartLimitBurst(v string) (string, []SystemdWarning, error) {
	n, _, err := lcUnsigned(v)
	if err != nil {
		return "", nil, err
	}
	return n, []SystemdWarning{{"NOTE", "StartLimitBurst= → restart-limit-count, which counts automatic restarts only; " +
		"systemd counts every start, the first and manual ones included"}}, nil
}

// lcSignalNames is systemd's static_signal_table: the names it takes,
// with or without "SIG", case-sensitive.
var lcSignalNames = map[string]bool{
	"HUP": true, "INT": true, "QUIT": true, "ILL": true, "TRAP": true, "ABRT": true, "BUS": true,
	"FPE": true, "KILL": true, "USR1": true, "SEGV": true, "USR2": true, "PIPE": true, "ALRM": true,
	"TERM": true, "STKFLT": true, "CHLD": true, "CONT": true, "STOP": true, "TSTP": true, "TTIN": true,
	"TTOU": true, "URG": true, "XCPU": true, "XFSZ": true, "VTALRM": true, "PROF": true, "WINCH": true,
	"IO": true, "PWR": true, "SYS": true,
}

// glibc's SIGRTMIN and SIGRTMAX on Linux: NPTL keeps 32 and 33.
const lcSigRTMin, lcSigRTMax = 34, 64

// lcSignalFrom is systemd's signal_from_string: a number 1..64, a name
// with or without "SIG", or RTMIN+n / RTMAX-n. It returns slinit's
// spelling, "SIG" and the name, or the number for a realtime signal,
// which slinit has no name for.
func lcSignalFrom(v string) (string, error) {
	if n, err := strconv.Atoi(v); err == nil {
		if n < 1 || n > lcSigRTMax {
			return "", fmt.Errorf("signal %d out of range", n)
		}
		return v, nil
	}
	s := strings.TrimPrefix(v, "SIG")
	if lcSignalNames[s] {
		return "SIG" + s, nil
	}
	for _, rt := range []struct {
		prefix string
		base   int
		sign   int
	}{{"RTMIN", lcSigRTMin, 1}, {"RTMAX", lcSigRTMax, -1}} {
		rest, ok := strings.CutPrefix(s, rt.prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return strconv.Itoa(rt.base), nil
		}
		want := "+"
		if rt.sign < 0 {
			want = "-"
		}
		if !strings.HasPrefix(rest, want) {
			break
		}
		n, err := strconv.Atoi(rest[1:])
		if err != nil || n < 0 || n > lcSigRTMax-lcSigRTMin {
			break
		}
		return strconv.Itoa(rt.base + rt.sign*n), nil
	}
	return "", fmt.Errorf("%q is no signal", v)
}

func lcSignal(v string) (string, []SystemdWarning, error) {
	s, err := lcSignalFrom(v)
	return s, nil, err
}

// lcExitStatusNames is systemd's exit_status_mappings: the names an
// exit status list takes for a number (src/shared/exit-status.h).
var lcExitStatusNames = map[string]int{
	"SUCCESS": 0, "FAILURE": 1,
	// LSB
	"INVALIDARGUMENT": 2, "NOTIMPLEMENTED": 3, "NOPERMISSION": 4, "NOTINSTALLED": 5,
	"NOTCONFIGURED": 6, "NOTRUNNING": 7,
	// BSD sysexits.h
	"USAGE": 64, "DATAERR": 65, "NOINPUT": 66, "NOUSER": 67, "NOHOST": 68, "UNAVAILABLE": 69,
	"SOFTWARE": 70, "OSERR": 71, "OSFILE": 72, "CANTCREAT": 73, "IOERR": 74, "TEMPFAIL": 75,
	"PROTOCOL": 76, "NOPERM": 77, "CONFIG": 78,
	// systemd's own; 223 and 234 are retired and have no name.
	"CHDIR": 200, "NICE": 201, "FDS": 202, "EXEC": 203, "MEMORY": 204, "LIMITS": 205,
	"OOM_ADJUST": 206, "SIGNAL_MASK": 207, "STDIN": 208, "STDOUT": 209, "CHROOT": 210,
	"IOPRIO": 211, "TIMERSLACK": 212, "SECUREBITS": 213, "SETSCHEDULER": 214, "CPUAFFINITY": 215,
	"GROUP": 216, "USER": 217, "CAPABILITIES": 218, "CGROUP": 219, "SETSID": 220, "CONFIRM": 221,
	"STDERR": 222, "PAM": 224, "NETWORK": 225, "NAMESPACE": 226, "NO_NEW_PRIVILEGES": 227,
	"SECCOMP": 228, "SELINUX_CONTEXT": 229, "PERSONALITY": 230, "APPARMOR": 231,
	"ADDRESS_FAMILIES": 232, "RUNTIME_DIRECTORY": 233, "CHOWN": 235, "SMACK_PROCESS_LABEL": 236,
	"KEYRING": 237, "STATE_DIRECTORY": 238, "CACHE_DIRECTORY": 239, "LOGS_DIRECTORY": 240,
	"CONFIGURATION_DIRECTORY": 241, "NUMA_POLICY": 242, "CREDENTIALS": 243, "BPF": 244, "KSM": 245,
	"MEMORY_THP": 246, "EXCEPTION": 255,
}

// lcStatusList converts one assignment of an exit status list, as
// config_parse_set_status reads it: words that are an exit status (a
// name above or 0..255) or else a signal. systemd logs a word it cannot
// parse and keeps the rest; so does this. withSignals says whether the
// slinit setting takes signals; skipZero leaves exit 0 out.
func lcStatusList(key string, withSignals, skipZero bool) func(string) (string, []SystemdWarning, error) {
	return func(v string) (string, []SystemdWarning, error) {
		var out []string
		var warns []SystemdWarning
		drop := func(w, why string) {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s: %s %s — left out", key, v, w, why)})
		}
		for _, w := range strings.Fields(v) {
			code, named := lcExitStatusNames[w]
			if !named && w != "" && strings.Trim(w, "0123456789") == "" {
				n, err := strconv.ParseUint(w, 10, 8)
				if err != nil {
					drop(w, "is no exit status (0..255), and systemd ignores it")
					continue
				}
				code, named = int(n), true
			}
			if named {
				if code != 0 || !skipZero {
					out = append(out, strconv.Itoa(code))
				}
				continue
			}
			sig, err := lcSignalFrom(w)
			switch {
			case err != nil:
				drop(w, "is no exit status or signal, and systemd ignores it")
			case !withSignals:
				drop(w, "is a signal, and slinit's list takes exit codes only")
			case !strings.HasPrefix(sig, "SIG"):
				// A bare number in normal-exit is an exit code.
				drop(w, "is a realtime signal, which normal-exit cannot name")
			default:
				out = append(out, sig)
			}
		}
		return strings.Join(out, " "), warns, nil
	}
}

// lcAction converts systemd's emergency actions to slinit's system
// actions. slinit's shutdown is always the orderly one, and has no
// soft-reboot or kexec as an action: those become a full reboot, the
// action the unit asks for or a heavier one, never a lighter one.
func lcAction(v string) (string, []SystemdWarning, error) {
	switch v {
	case "none", "reboot", "poweroff", "halt":
		return v, nil, nil
	case "exit":
		return v, []SystemdWarning{{"NOTE", "action exit: systemd on a host (not in a container) powers off instead; " +
			"slinit as PID 1 ignores it, and ends only a user or container instance"}}, nil
	}
	base, note := "", ""
	switch v {
	case "reboot-force", "reboot-immediate", "poweroff-force", "poweroff-immediate",
		"halt-force", "halt-immediate", "exit-force":
		base = v[:strings.IndexByte(v, '-')]
		note = "slinit stops the services in order first"
		if base == "exit" {
			note += "; systemd on a host powers off instead, and slinit as PID 1 ignores exit"
		}
	case "soft-reboot", "soft-reboot-force", "kexec", "kexec-force":
		base = "reboot"
		note = "slinit has no " + strings.TrimSuffix(v, "-force") + " action, so the machine reboots"
	default:
		return "", nil, fmt.Errorf("%q is no emergency action", v)
	}
	return base, []SystemdWarning{{"NOTE", fmt.Sprintf("action %s → %s: %s", v, base, note)}}, nil
}

func lcKillMode(v string) (string, []SystemdWarning, error) {
	switch v {
	case "control-group", "mixed":
		return v, nil, nil
	case "process":
		return v, []SystemdWarning{{"NOTE", "KillMode=process → kill-mode = process; slinit signals the main " +
			"process's process group, not the main process alone (options = signal-process-only does that), " +
			"and its final-kill-signal always reaches the group"}}, nil
	case "none":
		return v, []SystemdWarning{{"NOTE", "KillMode=none → kill-mode = none; unlike systemd, slinit still sends " +
			"final-kill-signal when the stop timeout passes"}}, nil
	}
	return "", nil, fmt.Errorf("%q is no kill mode", v)
}

func lcRestartMode(v string) (string, []SystemdWarning, error) {
	switch v {
	case "normal", "direct":
		return v, nil, nil
	case "debug":
		return "normal", []SystemdWarning{{"NOTE", "RestartMode=debug → restart-mode = normal; slinit has no " +
			"debug-logged restarts and sets no $DEBUG_INVOCATION"}}, nil
	}
	return "", nil, fmt.Errorf("%q is no restart mode", v)
}

func lcStopFailureMode(v string) (string, []SystemdWarning, error) {
	if v == "abort" {
		return v, []SystemdWarning{{"NOTE", "TimeoutStopFailureMode=abort: slinit waits timeout-abort-sec after the " +
			"SIGABRT before final-kill-signal, 0 unless set by hand; systemd waits TimeoutAbortSec=, else TimeoutStopSec="}}, nil
	}
	return v, nil, nil
}

func lcExitType(v string) (string, []SystemdWarning, error) {
	if v == "cgroup" {
		return v, []SystemdWarning{{"NOTE", "ExitType=cgroup → exit-type = cgroup; slinit waits for the cgroup to " +
			"empty only when the service has one (cgroup, slice or --cgroup-path), and for 30 s at most"}}, nil
	}
	return v, nil, nil
}

// lcFDStorePreserve: slinit's "no" keeps the store across a restart and
// releases it on a final stop, which is systemd's "restart" (its
// default). systemd's "no" releases it on every stop; slinit has none.
func lcFDStorePreserve(v string) (string, []SystemdWarning, error) {
	switch v {
	case "restart":
		return "no", nil, nil
	case "on-success":
		return v, nil, nil
	}
	b, err := lcParseBool(v)
	if err != nil {
		return "", nil, fmt.Errorf("%q is none of no, yes, restart, on-success", v)
	}
	if b {
		return "yes", nil, nil
	}
	return "no", []SystemdWarning{{"NOTE", "FileDescriptorStorePreserve=" + v + " → file-descriptor-store-preserve = no, " +
		"which keeps the store across a restart; systemd releases it on every stop"}}, nil
}

// lcBusName records BusName=, whose specifiers systemd expands; Type=
// may come later, so lcFinishBusName decides.
func lcBusName(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v, warns := expandSpecifiers(cfg, key, strings.TrimSpace(trimQuotes(val)))
	err := validateSlinitSetting("bus-name", v)
	if v == "" {
		// No bus name in systemd, which ignores the line; slinit
		// would read it as clearing an earlier one.
		err = fmt.Errorf("an empty bus name is invalid")
	}
	if err != nil {
		return append(warns, SystemdWarning{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)})
	}
	st := lcStateOf(cfg)
	st.busName, st.busNameRaw = v, strings.TrimSpace(trimQuotes(val))
	return warns
}

// lcFinishBusName: slinit's bus-name does one thing, it holds the start
// until the name is on the bus (a dbus-send ready check, whenever
// dbus-send is installed). That is systemd's Type=dbus. For any other
// type systemd only records the name, and a gate there would hold every
// start of the service on a bus that may not be up, so the name is not
// carried. slinit offers no option to keep the name without the gate.
func lcFinishBusName(cfg *SystemdConfig, st *lcState) []SystemdWarning {
	if st.busName == "" {
		return nil
	}
	if cfg.unitType != "dbus" {
		return []SystemdWarning{{"NOTE", "BusName=" + st.busNameRaw + " not carried: slinit's bus-name holds the start " +
			"until the name is on the bus, which systemd does for Type=dbus only"}}
	}
	cfg.setMapped("bus-name", st.busName)
	return []SystemdWarning{{"NOTE", "BusName= → bus-name; slinit counts the service started once the name is on the " +
		"system bus, polling with dbus-send (without dbus-send it does not wait)"}}
}

// lcRebootArgument maps RebootArgument=, whose specifiers systemd
// expands. slinit expands $VAR in reboot-argument, so a $ is doubled.
func lcRebootArgument(cfg *SystemdConfig, key, val string) []SystemdWarning {
	v, warns := expandSpecifiers(cfg, key, strings.TrimSpace(trimQuotes(val)))
	return append(warns, cfg.applyRule("Service", key, v, sdRule{key: "reboot-argument", conv: func(v string) (string, []SystemdWarning, error) {
		if v == "" {
			return "", nil, nil
		}
		return strings.ReplaceAll(v, "$", "$$"), []SystemdWarning{{"NOTE", "RebootArgument= → reboot-argument; " +
			"slinit logs it but does not pass it to reboot(2) yet"}}, nil
	}})...)
}

// lcRemainAfterExit: systemd keeps a unit with RemainAfterExit=yes
// active after its processes exit. That is what slinit's scripted type,
// Type=oneshot's mapping, does anyway; a process or bgprocess service
// stops when its process exits, and slinit has nothing to keep it
// started. Type= may come later in the unit, so the note says both.
func lcRemainAfterExit(_ *SystemdConfig, key, val string) []SystemdWarning {
	b, err := lcParseBool(strings.TrimSpace(trimQuotes(val)))
	if err != nil {
		return []SystemdWarning{{"WARN", fmt.Sprintf("[Service] %s=%s ignored: %v", key, val, err)}}
	}
	if !b {
		return nil
	}
	return []SystemdWarning{{"NOTE", "RemainAfterExit=" + val + ": with Type=oneshot slinit's scripted service stays " +
		"started after its command exits, as asked; with any other Type= slinit has no equivalent, and the service " +
		"stops when its process exits"}}
}

// lcLoneRef is a word that is one $VAR reference, which systemd splits
// into words once expanded.
var lcLoneRef = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*$`)

// lcShellCommand writes argv, in slinit's $ syntax, as the shell
// command an exec-condition runs with /bin/sh -c. Every word is single
// quoted, so the shell takes it as it is once slinit has expanded its
// variables at load (a value holding a quote would break out; they come
// from slinit's own environment); a lone $VAR is left bare, so that the
// shell splits its value as systemd does.
func lcShellCommand(argv []string) (string, bool) {
	parts := make([]string, len(argv))
	for i, w := range argv {
		if strings.ContainsAny(w, "\n\r") {
			return "", false
		}
		if lcLoneRef.MatchString(w) {
			parts[i] = w
			continue
		}
		parts[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(parts, " "), true
}

// lcExecCondition maps each ExecCondition= command to an exec-condition
// of its own: slinit checks every one, as systemd runs them in turn.
func lcExecCondition(cfg *SystemdConfig, key, val string) []SystemdWarning {
	if strings.TrimSpace(val) == "" {
		cfg.dropMapped("exec-condition")
		return nil
	}
	cmds, warns, err := parseExecLine(cfg, key, val)
	if err != nil {
		cfg.errs = append(cfg.errs, err.Error())
		return warns
	}
	for _, c := range cmds {
		argv := c.cmd.argv
		if c.ignoreFailure {
			// systemd counts any failure of a "-" command as success
			// before it looks at the exit status, so the condition holds.
			argv = ignoreFailureArgv(argv)
		}
		value, ok := lcShellCommand(argv)
		if !ok {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf(
				"%s=%s: an argument holds a newline, which an exec-condition cannot — condition dropped", key, val)})
			continue
		}
		// "+" runs it as root; so does "!", which keeps the sandbox in
		// systemd, but slinit gives a condition no sandbox either way.
		value = privPrefix(c.fullPriv || c.noSetuid) + value
		if err := validateSlinitSetting("exec-condition", value); err != nil {
			warns = append(warns, SystemdWarning{"WARN", fmt.Sprintf("%s=%s ignored: %v", key, val, err)})
			continue
		}
		warns = append(warns, warnArgv0(key, c.cmd.argv0)...)
		warns = append(warns, warnExecVars(key, c.refs, c.bad)...)
		cfg.mapped = append(cfg.mapped, kvPair{"exec-condition", value})
	}
	return append(warns, SystemdWarning{"NOTE", "ExecCondition=" + val + " → exec-condition, run with /bin/sh -c for at " +
		"most 10 s; any failure skips the service, where systemd fails the start on exit 255 or a signal"})
}
