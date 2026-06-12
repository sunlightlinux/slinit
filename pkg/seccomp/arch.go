// Package seccomp builds and installs seccomp-bpf filters for slinit.
//
// The package is consumed in two places:
//   - pkg/config validates and group-expands user filter specs at parse
//     time so configuration errors are caught before boot, not in the
//     child after fork.
//   - cmd/slinit-runner compiles the expanded spec into a BPF program
//     and installs it via the seccomp(2) syscall right before exec, in
//     the same task that will become the service.
//
// The cBPF program operates on struct seccomp_data: nr at offset 0 and
// arch at offset 4. We first verify the architecture matches one of the
// allowed AUDIT_ARCH_* values, then dispatch on the syscall number.
package seccomp

import (
	"fmt"
	"runtime"
	"strings"
)

// AUDIT_ARCH_* values from <linux/audit.h>. Kept here rather than
// imported from golang.org/x/sys/unix so this package compiles cleanly
// in unit tests on non-Linux developer hosts (the actual install path
// in cmd/slinit-runner is Linux-only by build tag).
const (
	auditArchX86_64  uint32 = 0xC000003E
	auditArchX86     uint32 = 0x40000003
	auditArchARM64   uint32 = 0xC00000B7
	auditArchARM     uint32 = 0x40000028
	auditArchAARCH64        = auditArchARM64 // common alias
)

// archAliases maps the operator-facing arch strings (matching systemd's
// SystemCallArchitectures= and ConditionArchitecture= naming) onto the
// canonical name used by archNumber.
var archAliases = map[string]string{
	"native":  nativeArch(),
	"x86-64":  "x86-64",
	"x86_64":  "x86-64",
	"amd64":   "x86-64",
	"x86":     "x86",
	"i386":    "x86",
	"i686":    "x86",
	"arm64":   "arm64",
	"aarch64": "arm64",
	"arm":     "arm",
}

// nativeArch returns the canonical arch name of the running binary so
// "native" can be resolved without a runtime syscall. The set of
// supported targets here matches the arches slinit ships binaries for.
func nativeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86-64"
	case "386":
		return "x86"
	case "arm64":
		return "arm64"
	case "arm":
		return "arm"
	default:
		return runtime.GOARCH
	}
}

// ResolveArch translates a user-supplied arch name into the canonical
// form. Unknown names produce a helpful error so a typo in
// system-call-architectures= fails at parse time instead of silently
// excluding the running arch (which would kill every syscall).
func ResolveArch(name string) (string, error) {
	canon, ok := archAliases[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return "", fmt.Errorf("unknown architecture %q (try native|x86-64|x86|arm64|arm)", name)
	}
	return canon, nil
}

// archNumber returns the AUDIT_ARCH_* constant the BPF filter compares
// the seccomp_data.arch field against. Returns 0 if the canonical name
// is not one we have a constant for.
func archNumber(canon string) uint32 {
	switch canon {
	case "x86-64":
		return auditArchX86_64
	case "x86":
		return auditArchX86
	case "arm64":
		return auditArchARM64
	case "arm":
		return auditArchARM
	default:
		return 0
	}
}

// NativeArch is the canonical name of the arch this binary was built
// for. Exported so callers can default to it without re-implementing
// the GOARCH switch.
func NativeArch() string { return nativeArch() }
