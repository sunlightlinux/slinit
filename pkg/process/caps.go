package process

import (
	"fmt"
	"strconv"
	"strings"
)

// Linux capability constants.
// These correspond to CAP_* values from <linux/capability.h>.
const (
	CapChown            = 0
	CapDacOverride      = 1
	CapDacReadSearch    = 2
	CapFowner           = 3
	CapFsetid           = 4
	CapKill             = 5
	CapSetgid           = 6
	CapSetuid           = 7
	CapSetpcap          = 8
	CapLinuxImmutable   = 9
	CapNetBindService   = 10
	CapNetBroadcast     = 11
	CapNetAdmin         = 12
	CapNetRaw           = 13
	CapIpcLock          = 14
	CapIpcOwner         = 15
	CapSysModule        = 16
	CapSysRawio         = 17
	CapSysChroot        = 18
	CapSysPtrace        = 19
	CapSysPacct         = 20
	CapSysAdmin         = 21
	CapSysBoot          = 22
	CapSysNice          = 23
	CapSysResource      = 24
	CapSysTime          = 25
	CapSysTtyConfig     = 26
	CapMknod            = 27
	CapLease            = 28
	CapAuditWrite       = 29
	CapAuditControl     = 30
	CapSetfcap          = 31
	CapMacOverride      = 32
	CapMacAdmin         = 33
	CapSyslog           = 34
	CapWakeAlarm        = 35
	CapBlockSuspend     = 36
	CapAuditRead        = 37
	CapPerfmon          = 38
	CapBpf              = 39
	CapCheckpointRestore = 40
)

// capNames maps capability string names to their numeric values.
var capNames = map[string]uintptr{
	"cap_chown":              CapChown,
	"cap_dac_override":       CapDacOverride,
	"cap_dac_read_search":    CapDacReadSearch,
	"cap_fowner":             CapFowner,
	"cap_fsetid":             CapFsetid,
	"cap_kill":               CapKill,
	"cap_setgid":             CapSetgid,
	"cap_setuid":             CapSetuid,
	"cap_setpcap":            CapSetpcap,
	"cap_linux_immutable":    CapLinuxImmutable,
	"cap_net_bind_service":   CapNetBindService,
	"cap_net_broadcast":      CapNetBroadcast,
	"cap_net_admin":          CapNetAdmin,
	"cap_net_raw":            CapNetRaw,
	"cap_ipc_lock":           CapIpcLock,
	"cap_ipc_owner":          CapIpcOwner,
	"cap_sys_module":         CapSysModule,
	"cap_sys_rawio":          CapSysRawio,
	"cap_sys_chroot":         CapSysChroot,
	"cap_sys_ptrace":         CapSysPtrace,
	"cap_sys_pacct":          CapSysPacct,
	"cap_sys_admin":          CapSysAdmin,
	"cap_sys_boot":           CapSysBoot,
	"cap_sys_nice":           CapSysNice,
	"cap_sys_resource":       CapSysResource,
	"cap_sys_time":           CapSysTime,
	"cap_sys_tty_config":     CapSysTtyConfig,
	"cap_mknod":              CapMknod,
	"cap_lease":              CapLease,
	"cap_audit_write":        CapAuditWrite,
	"cap_audit_control":      CapAuditControl,
	"cap_setfcap":            CapSetfcap,
	"cap_mac_override":       CapMacOverride,
	"cap_mac_admin":          CapMacAdmin,
	"cap_syslog":             CapSyslog,
	"cap_wake_alarm":         CapWakeAlarm,
	"cap_block_suspend":      CapBlockSuspend,
	"cap_audit_read":         CapAuditRead,
	"cap_perfmon":            CapPerfmon,
	"cap_bpf":                CapBpf,
	"cap_checkpoint_restore": CapCheckpointRestore,
}

// ParseCapabilities parses a comma/space-separated list of capability names
// into a slice of capability numbers suitable for SysProcAttr.AmbientCaps.
// Names are case-insensitive, with or without the "cap_" prefix.
func ParseCapabilities(s string) ([]uintptr, error) {
	if s == "" {
		return nil, nil
	}

	// Split on commas and/or whitespace
	s = strings.ReplaceAll(s, ",", " ")
	parts := strings.Fields(s)

	caps := make([]uintptr, 0, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}

		// Try with cap_ prefix
		if val, ok := capNames[name]; ok {
			caps = append(caps, val)
			continue
		}
		// Try adding cap_ prefix
		if val, ok := capNames["cap_"+name]; ok {
			caps = append(caps, val)
			continue
		}
		// Try numeric
		if n, err := strconv.Atoi(name); err == nil && n >= 0 && n <= 40 {
			caps = append(caps, uintptr(n))
			continue
		}

		return nil, fmt.Errorf("unknown capability: %s", part)
	}

	return caps, nil
}

// Securebits constants from <linux/securebits.h>.
const (
	SecbitNoroot             uint32 = 1 << 0
	SecbitNorootLocked       uint32 = 1 << 1
	SecbitNoSetuidFixup      uint32 = 1 << 2
	SecbitNoSetuidFixupLocked uint32 = 1 << 3
	SecbitKeepCaps           uint32 = 1 << 4
	SecbitKeepCapsLocked     uint32 = 1 << 5
	SecbitNoCapAmbientRaise  uint32 = 1 << 6
	SecbitNoCapAmbientRaiseLocked uint32 = 1 << 7
)

// secbitNames maps securebits string names to their values.
var secbitNames = map[string]uint32{
	"noroot":                       SecbitNoroot,
	"noroot-locked":                SecbitNorootLocked,
	"no-setuid-fixup":              SecbitNoSetuidFixup,
	"no-setuid-fixup-locked":       SecbitNoSetuidFixupLocked,
	"keep-caps":                    SecbitKeepCaps,
	"keep-caps-locked":             SecbitKeepCapsLocked,
	"no-cap-ambient-raise":         SecbitNoCapAmbientRaise,
	"no-cap-ambient-raise-locked":  SecbitNoCapAmbientRaiseLocked,
}

// ParseSecurebits parses a space-separated list of securebits flag names
// into a combined bitmask.
func ParseSecurebits(s string) (uint32, error) {
	if s == "" {
		return 0, nil
	}

	var bits uint32
	for _, name := range strings.Fields(s) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		val, ok := secbitNames[name]
		if !ok {
			return 0, fmt.Errorf("unknown securebits flag: %s", name)
		}
		bits |= val
	}
	return bits, nil
}
