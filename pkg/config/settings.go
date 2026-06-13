// Package config implements the dinit-compatible service configuration file parser.
package config

import "strings"

// OperatorType identifies what assignment operators a setting supports.
type OperatorType uint8

const (
	OpEquals    OperatorType = 1 << iota // setting = value
	OpColon                              // setting: value
	OpPlusEqual                          // setting += value
)

// SettingInfo describes a recognized setting.
type SettingInfo struct {
	Name     string
	Operator OperatorType
}

// KnownSettings maps setting names to their allowed operators.
// This matches dinit's setting registry from load-service.cc.
var KnownSettings = map[string]OperatorType{
	// Service identity
	"type":        OpEquals,
	"description": OpEquals,
	"author":      OpEquals,
	"version":     OpEquals,
	"usage":       OpEquals,

	// Dependencies (use colon)
	"depends-on":    OpColon,
	"depends-ms":    OpColon,
	"waits-for":     OpColon,
	"prepared-by":   OpColon,
	"depends-on.d":  OpColon,
	"depends-ms.d":  OpColon,
	"waits-for.d":   OpColon,
	"prepared-by.d": OpColon,
	"before":        OpColon,
	"after":         OpColon,

	// Commands
	"command":      OpEquals | OpPlusEqual,
	"stop-command": OpEquals | OpPlusEqual,

	// Working directory
	"working-dir": OpEquals,

	// Environment
	"env-file": OpEquals,

	// Process management
	"run-as":                 OpEquals,
	"manual":                 OpEquals,
	"restart":                OpEquals,
	"smooth-recovery":        OpEquals,
	"normal-exit":            OpEquals | OpPlusEqual,
	"stop-timeout":           OpEquals,
	"start-timeout":          OpEquals,
	"restart-delay":          OpEquals,
	"restart-delay-step":     OpEquals,
	"restart-delay-cap":      OpEquals,
	"restart-limit-interval": OpEquals,
	"restart-limit-count":    OpEquals,
	"term-signal":            OpEquals,
	"termsignal":             OpEquals, // deprecated alias (dinit compat)
	"stopsig":                OpEquals, // OpenRC alias
	"reload-signal":          OpEquals, // upstart-inspired: signal sent by `slinitctl reload-signal`
	"pid-file":               OpEquals,
	"ready-notification":     OpEquals,
	"watchdog-timeout":       OpEquals,

	// Logging
	"logfile":             OpEquals,
	"log-type":            OpEquals,
	"log-buffer-size":     OpEquals,
	"logfile-permissions": OpEquals,
	"logfile-uid":         OpEquals,
	"logfile-gid":         OpEquals,

	// Socket activation
	"socket-listen":      OpEquals | OpPlusEqual, // multiple sockets via +=
	"socket-permissions": OpEquals,
	"socket-uid":         OpEquals,
	"socket-gid":         OpEquals,
	"socket-activation":  OpEquals, // "immediate" (default) or "on-demand"

	// Chaining
	"chain-to": OpEquals,

	// Options (flags)
	"options": OpEquals | OpPlusEqual,

	// Alias
	"provides": OpEquals,

	// Consumer (dinit uses =, slinit originally used :, accept both)
	"consumer-of":   OpEquals | OpColon,
	"shared-logger": OpEquals, // name of shared logger service (multi-service log mux)

	// Load options
	"load-options": OpEquals | OpPlusEqual,

	// rlimits
	"rlimit-nofile":    OpEquals,
	"rlimit-core":      OpEquals,
	"rlimit-data":      OpEquals,
	"rlimit-as":        OpEquals,
	"rlimit-addrspace": OpEquals, // dinit compat alias for rlimit-as

	// cgroup
	"cgroup":        OpEquals,
	"run-in-cgroup": OpEquals, // dinit compat alias for cgroup

	// cgroup v2 resource limits
	"cgroup-memory-max":  OpEquals,
	"cgroup-memory-high": OpEquals,
	"cgroup-memory-min":  OpEquals,
	"cgroup-memory-low":  OpEquals,
	"cgroup-swap-max":    OpEquals,
	"cgroup-pids-max":    OpEquals,
	"cgroup-cpu-weight":  OpEquals,
	"cgroup-cpu-max":     OpEquals,
	"cgroup-io-weight":   OpEquals,
	"cgroup-cpuset-cpus": OpEquals,
	"cgroup-cpuset-mems": OpEquals,
	"cgroup-hugetlb":     OpEquals,
	"cgroup-setting":     OpEquals | OpPlusEqual, // generic: file value

	// nice/ioprio
	"nice":          OpEquals,
	"ioprio":        OpEquals,
	"oom-score-adj": OpEquals,

	// per-service file-creation mask
	"umask": OpEquals,

	// AppArmor confinement
	"apparmor-load":   OpEquals,
	"apparmor-switch": OpEquals,

	// systemd-style auto-managed service directories
	"runtime-directory":            OpEquals,
	"state-directory":              OpEquals,
	"cache-directory":              OpEquals,
	"logs-directory":               OpEquals,
	"configuration-directory":      OpEquals,
	"runtime-directory-mode":       OpEquals,
	"state-directory-mode":         OpEquals,
	"cache-directory-mode":         OpEquals,
	"logs-directory-mode":          OpEquals,
	"configuration-directory-mode": OpEquals,
	"runtime-directory-preserve":   OpEquals,

	// developer debug: SIGSTOP child before exec
	"debug": OpEquals,

	// path-based activation (the four are mutually exclusive per service)
	"start-on-path-exists":         OpEquals,
	"start-on-path-changed":        OpEquals,
	"start-on-path-modified":       OpEquals,
	"start-on-directory-not-empty": OpEquals,

	// cpu affinity
	"cpu-affinity": OpEquals,

	// real-time scheduling (telco / 5G data plane)
	"sched-policy":        OpEquals,
	"sched-priority":      OpEquals,
	"sched-runtime":       OpEquals,
	"sched-deadline":      OpEquals,
	"sched-period":        OpEquals,
	"sched-reset-on-fork": OpEquals,

	// memory locking + NUMA placement (telco / 5G data plane)
	"mlockall":       OpEquals,
	"numa-mempolicy": OpEquals,
	"numa-nodes":     OpEquals,

	// capabilities
	"capabilities": OpEquals | OpPlusEqual,
	"securebits":   OpEquals | OpPlusEqual,

	// utmp
	"inittab-id":   OpEquals,
	"inittab-line": OpEquals,

	// Extra commands (OpenRC-style custom actions)
	"extra-command":         OpEquals,
	"extra-started-command": OpEquals,

	// Runit-inspired features
	"finish-command":       OpEquals | OpPlusEqual,
	"ready-check-command":  OpEquals | OpPlusEqual,
	"ready-check-interval": OpEquals,
	"pre-stop-hook":        OpEquals | OpPlusEqual,
	"env-dir":              OpEquals,
	"chroot":               OpEquals,
	"lock-file":            OpEquals,
	"new-session":          OpEquals,
	"namespace-pid":        OpEquals,
	"namespace-mount":      OpEquals,
	"namespace-net":        OpEquals,
	"namespace-uts":        OpEquals,
	"namespace-ipc":        OpEquals,
	"namespace-user":       OpEquals,
	"namespace-cgroup":     OpEquals,
	"namespace-uid-map":    OpEquals | OpPlusEqual,
	"namespace-gid-map":    OpEquals | OpPlusEqual,
	"close-stdin":          OpEquals,
	"close-stdout":         OpEquals,
	"close-stderr":         OpEquals,

	// Pre-start fail-fast path checks (OpenRC-inspired)
	"required-files": OpEquals | OpPlusEqual,
	"required-dirs":  OpEquals | OpPlusEqual,

	// systemd-style seccomp-bpf filter (#4). The filter list supports
	// '~' first-item prefix for deny mode, '@group' tokens for the
	// curated groups in pkg/seccomp, and bare syscall names. Repeatable
	// with '+='.
	"system-call-filter":        OpEquals | OpPlusEqual,
	"system-call-architectures": OpEquals | OpPlusEqual,
	"system-call-error-number":  OpEquals,
	"system-call-log":           OpEquals | OpPlusEqual,

	// systemd-style Restrict*/Protect* hardening cluster (#7 v1).
	// Each is a yes/no toggle. Some apply via an additional seccomp
	// deny filter, some via extra mount ops in the runner; several do
	// both. Combined with the user's system-call-filter via the
	// kernel's "most-restrictive of all loaded filters wins" rule.
	"protect-kernel-tunables": OpEquals,
	"protect-kernel-modules":  OpEquals,
	"protect-kernel-logs":     OpEquals,
	"protect-clock":           OpEquals,
	"protect-control-groups":  OpEquals,
	"protect-hostname":        OpEquals,
	"lock-personality":        OpEquals,

	// systemd-style filesystem sandbox (applied via slinit-runner in a
	// fresh mount namespace; CLONE_NEWNS is auto-implied)
	"private-tmp":          OpEquals,
	"protect-system":       OpEquals,
	"read-only-paths":      OpEquals | OpPlusEqual,
	"read-write-paths":     OpEquals | OpPlusEqual,
	"protect-home":         OpEquals,
	"inaccessible-paths":   OpEquals | OpPlusEqual,
	"protect-proc":         OpEquals,
	"proc-subset":          OpEquals,
	"bind-paths":           OpEquals | OpPlusEqual,
	"bind-read-only-paths": OpEquals | OpPlusEqual,
	"temporary-filesystem": OpEquals | OpPlusEqual,

	// Virtual TTY (screen-like attach/detach)
	"vtty":            OpEquals, // true/false
	"vtty-scrollback": OpEquals, // scrollback buffer size in bytes

	// Cron-like periodic tasks
	"cron-command":  OpEquals | OpPlusEqual,
	"cron-interval": OpEquals,
	"cron-delay":    OpEquals,
	"cron-on-error": OpEquals,

	// Continuous health checking
	"healthcheck-command":      OpEquals | OpPlusEqual,
	"healthcheck-interval":     OpEquals,
	"healthcheck-delay":        OpEquals,
	"healthcheck-max-failures": OpEquals,
	"unhealthy-command":        OpEquals | OpPlusEqual,

	// Platform keywords (OpenRC-compatible)
	"keyword": OpEquals,

	// Output/error logger (OpenRC OUTPUT_LOGGER / ERROR_LOGGER)
	"output-logger": OpEquals | OpPlusEqual,
	"error-logger":  OpEquals | OpPlusEqual,

	// Log rotation and filtering
	"logfile-max-size":    OpEquals,
	"logfile-max-files":   OpEquals,
	"logfile-rotate-time": OpEquals,
	"log-processor":       OpEquals | OpPlusEqual,
	"log-include":         OpEquals,
	"log-exclude":         OpEquals,
}

// IsKnownSetting returns true if the setting name is recognized.
func IsKnownSetting(name string) bool {
	_, ok := KnownSettings[name]
	if ok {
		return true
	}
	// Dynamic prefix: control-command-SIGNAL (e.g., control-command-HUP)
	if strings.HasPrefix(name, "control-command-") {
		return true
	}
	// Dynamic prefix: condition-X / assert-X start predicates. The
	// suffix is validated when the predicate is parsed, so unknown
	// kinds still surface as parse errors with a useful name.
	if isPredicateSetting(name) {
		return true
	}
	return false
}

// ValidOperator checks if the given operator is valid for the setting.
func ValidOperator(setting string, op OperatorType) bool {
	allowed, ok := KnownSettings[setting]
	if ok {
		return allowed&op != 0
	}
	// Dynamic prefix: control-command-SIGNAL accepts = and +=
	if strings.HasPrefix(setting, "control-command-") {
		return (OpEquals|OpPlusEqual)&op != 0
	}
	// Predicates accept '=' only — the value is a single string.
	if isPredicateSetting(setting) {
		return op&OpEquals != 0
	}
	return false
}

func isPredicateSetting(name string) bool {
	return strings.HasPrefix(name, "condition-") || strings.HasPrefix(name, "assert-")
}

// OptionFlags maps option string names to their ServiceFlags field names.
var OptionFlags = map[string]string{
	"runs-on-console":     "RunsOnConsole",
	"starts-on-console":   "StartsOnConsole",
	"shares-console":      "SharesConsole",
	"pass-cs-fd":          "PassCSFD",
	"start-interruptible": "StartInterruptible",
	"skippable":           "Skippable",
	"signal-process-only": "SignalProcessOnly",
	"always-chain":        "AlwaysChain",
	"kill-all-on-stop":    "KillAllOnStop",
	"unmask-intr":         "UnmaskIntr",
	"starts-rwfs":         "RWReady",
	"starts-log":          "LogReady",
}
