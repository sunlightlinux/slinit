// Package config implements the dinit-compatible service configuration file parser.
package config

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
	"command":      OpEquals,
	"stop-command": OpEquals,

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
	"restart-delay":      OpEquals,
	"restart-limit-interval": OpEquals,
	"restart-limit-count":    OpEquals,
	"term-signal":        OpEquals,
	"pid-file":           OpEquals,
	"ready-notification": OpEquals,

	// Logging
	"logfile":    OpEquals,
	"log-type":   OpEquals,
	"log-buffer-size": OpEquals,

	// Socket activation
	"socket-listen": OpEquals,
	"socket-permissions": OpEquals,
	"socket-uid":    OpEquals,
	"socket-gid":    OpEquals,

	// Chaining
	"chain-to": OpEquals,

	// Options (flags)
	"options": OpEquals | OpPlusEqual,

	// Consumer
	"consumer-of": OpColon,

	// Load options
	"load-options": OpEquals | OpPlusEqual,

	// rlimits
	"rlimit-nofile":  OpEquals,
	"rlimit-core":    OpEquals,
	"rlimit-data":    OpEquals,
	"rlimit-as":      OpEquals,

	// cgroup
	"cgroup": OpEquals,

	// nice/ioprio
	"nice":   OpEquals,
	"ioprio": OpEquals,
	"oom-score-adj": OpEquals,
}

// IsKnownSetting returns true if the setting name is recognized.
func IsKnownSetting(name string) bool {
	_, ok := KnownSettings[name]
	return ok
}

// ValidOperator checks if the given operator is valid for the setting.
func ValidOperator(setting string, op OperatorType) bool {
	allowed, ok := KnownSettings[setting]
	if !ok {
		return false
	}
	return allowed&op != 0
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
}
