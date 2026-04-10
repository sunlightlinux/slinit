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

	// Dependencies (use colon)
	"depends-on":    OpColon,
	"depends-ms":    OpColon,
	"waits-for":     OpColon,
	"depends-on.d":  OpColon,
	"depends-ms.d":  OpColon,
	"waits-for.d":   OpColon,
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
	"run-as":             OpEquals,
	"restart":            OpEquals,
	"smooth-recovery":    OpEquals,
	"stop-timeout":       OpEquals,
	"start-timeout":      OpEquals,
	"restart-delay":          OpEquals,
	"restart-delay-step":     OpEquals,
	"restart-delay-cap":      OpEquals,
	"restart-limit-interval": OpEquals,
	"restart-limit-count":    OpEquals,
	"term-signal":        OpEquals,
	"termsignal":         OpEquals, // deprecated alias (dinit compat)
	"stopsig":            OpEquals, // OpenRC alias
	"pid-file":           OpEquals,
	"ready-notification": OpEquals,

	// Logging
	"logfile":             OpEquals,
	"log-type":            OpEquals,
	"log-buffer-size":     OpEquals,
	"logfile-permissions": OpEquals,
	"logfile-uid":         OpEquals,
	"logfile-gid":         OpEquals,

	// Socket activation
	"socket-listen":      OpEquals | OpPlusEqual, // multiple sockets via +=
	"socket-permissions":  OpEquals,
	"socket-uid":          OpEquals,
	"socket-gid":          OpEquals,
	"socket-activation":   OpEquals, // "immediate" (default) or "on-demand"

	// Chaining
	"chain-to": OpEquals,

	// Options (flags)
	"options": OpEquals | OpPlusEqual,

	// Alias
	"provides": OpEquals,

	// Consumer (dinit uses =, slinit originally used :, accept both)
	"consumer-of":    OpEquals | OpColon,
	"shared-logger":  OpEquals, // name of shared logger service (multi-service log mux)

	// Load options
	"load-options": OpEquals | OpPlusEqual,

	// rlimits
	"rlimit-nofile":     OpEquals,
	"rlimit-core":       OpEquals,
	"rlimit-data":       OpEquals,
	"rlimit-as":         OpEquals,
	"rlimit-addrspace":  OpEquals, // dinit compat alias for rlimit-as

	// cgroup
	"cgroup":         OpEquals,
	"run-in-cgroup":  OpEquals, // dinit compat alias for cgroup

	// nice/ioprio
	"nice":   OpEquals,
	"ioprio": OpEquals,
	"oom-score-adj": OpEquals,

	// cpu affinity
	"cpu-affinity": OpEquals,

	// capabilities
	"capabilities": OpEquals | OpPlusEqual,
	"securebits":   OpEquals | OpPlusEqual,

	// utmp
	"inittab-id":   OpEquals,
	"inittab-line": OpEquals,

	// Runit-inspired features
	"finish-command":      OpEquals | OpPlusEqual,
	"ready-check-command": OpEquals | OpPlusEqual,
	"ready-check-interval": OpEquals,
	"pre-stop-hook":       OpEquals | OpPlusEqual,
	"env-dir":             OpEquals,
	"chroot":              OpEquals,
	"lock-file":           OpEquals,
	"new-session":         OpEquals,
	"namespace-pid":       OpEquals,
	"namespace-mount":     OpEquals,
	"namespace-net":       OpEquals,
	"namespace-uts":       OpEquals,
	"namespace-ipc":       OpEquals,
	"namespace-user":      OpEquals,
	"namespace-cgroup":    OpEquals,
	"namespace-uid-map":   OpEquals | OpPlusEqual,
	"namespace-gid-map":   OpEquals | OpPlusEqual,
	"close-stdin":         OpEquals,
	"close-stdout":        OpEquals,
	"close-stderr":        OpEquals,

	// Pre-start fail-fast path checks (OpenRC-inspired)
	"required-files": OpEquals | OpPlusEqual,
	"required-dirs":  OpEquals | OpPlusEqual,

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
	return false
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
