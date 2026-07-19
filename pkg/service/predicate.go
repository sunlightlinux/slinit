package service

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PredicateKind identifies one of the systemd-style start preconditions.
type PredicateKind uint8

const (
	PredPathExists        PredicateKind = iota // path exists (any kind)
	PredPathExistsGlob                         // glob has at least one match
	PredPathIsDirectory                        // path exists and is a directory
	PredPathIsMountPoint                       // path is a mount point (parent dev differs)
	PredFileNotEmpty                           // regular file, size > 0
	PredDirectoryNotEmpty                      // directory containing at least one entry
	PredKernelCommandLine                      // /proc/cmdline contains the token
	PredVirtualization                         // running under (or not) a given virt
	PredFirstBoot                              // OS first-boot marker present
	PredHost                                   // host matches
	PredSecurity                               // a security framework is active
	PredNeedsUpdate                            // distro update marker present
	PredACPower                                // on AC power (not battery)
	PredPathIsSocket                           // path exists and is a socket (S_ISSOCK)
	PredFraction                               // fleet rollout: machine-id⊕tag hash < percent
	// Bucket A1: cheap-win predicates — all self-contained, all read a
	// single /proc or /etc source and compare a value the operator
	// declared. Systemd equivalents in parentheses.
	PredArchitecture       // Architecture= — GOARCH match (x86_64, arm64, ...)
	PredCPUFeature         // CPUFeature= — /proc/cpuinfo flag match
	PredCPUs               // CPUs= — runtime.NumCPU vs OP-value
	PredMemory             // Memory= — /proc/meminfo MemTotal vs OP-value
	PredKernelVersion      // KernelVersion= — uname().Release vs OP-value
	PredKernelModuleLoaded // KernelModuleLoaded= — /proc/modules match
	PredOSRelease          // OSRelease= — /etc/os-release KEY=VALUE
	PredUser               // User= — os.Getuid() vs uid or username
	PredGroup              // Group= — os.Getgid()+groups vs gid or groupname
	PredEnvironment        // Environment= — daemon env KEY=VALUE
	// Bucket A2: mid-complexity predicates. Each reads a specific
	// sysfs/procfs/etc source and interprets a small format.
	PredFileIsExecutable     // FileIsExecutable= — regular file with any exec bit set
	PredPathIsSymbolicLink   // PathIsSymbolicLink= — lstat + S_ISLNK
	PredPathIsReadWrite      // PathIsReadWrite= — statfs, MS_RDONLY not set
	PredFirmware             // Firmware= — uefi | bios | device-tree | smbios | DMI keys
	PredMachineTag           // MachineTag= — TAGS= line from /etc/machine-info
	PredCredential           // Credential= — file present under $CREDENTIALS_DIRECTORY
	PredControlGroupController // ControlGroupController= — cgroup v2 controller enabled
)

// Predicate is one declarative start precondition. A failing condition
// (IsAssert=false) skips the service silently — it transitions to
// STARTED with a "skipped" marker but no process runs and the service
// stays in STARTED so dependents proceed. A failing assertion
// (IsAssert=true) fails the start and cascades to dependents like any
// other start failure.
//
// Negate flips the truth value: a negated predicate succeeds when the
// underlying check fails (matching systemd's leading-! syntax).
type Predicate struct {
	Kind     PredicateKind
	Param    string
	Negate   bool
	IsAssert bool
}

// String returns the user-facing config name for diagnostics. The
// kebab-case form mirrors what the parser accepts.
func (p Predicate) String() string {
	var prefix string
	if p.IsAssert {
		prefix = "assert-"
	} else {
		prefix = "condition-"
	}
	var name string
	switch p.Kind {
	case PredPathExists:
		name = "path-exists"
	case PredPathExistsGlob:
		name = "path-exists-glob"
	case PredPathIsDirectory:
		name = "path-is-directory"
	case PredPathIsMountPoint:
		name = "path-is-mount-point"
	case PredFileNotEmpty:
		name = "file-not-empty"
	case PredDirectoryNotEmpty:
		name = "directory-not-empty"
	case PredKernelCommandLine:
		name = "kernel-command-line"
	case PredVirtualization:
		name = "virtualization"
	case PredFirstBoot:
		name = "first-boot"
	case PredHost:
		name = "host"
	case PredSecurity:
		name = "security"
	case PredNeedsUpdate:
		name = "needs-update"
	case PredACPower:
		name = "ac-power"
	case PredPathIsSocket:
		name = "path-is-socket"
	case PredFraction:
		name = "fraction"
	case PredArchitecture:
		name = "architecture"
	case PredCPUFeature:
		name = "cpu-feature"
	case PredCPUs:
		name = "cpus"
	case PredMemory:
		name = "memory"
	case PredKernelVersion:
		name = "kernel-version"
	case PredKernelModuleLoaded:
		name = "kernel-module-loaded"
	case PredOSRelease:
		name = "os-release"
	case PredUser:
		name = "user"
	case PredGroup:
		name = "group"
	case PredEnvironment:
		name = "environment"
	case PredFileIsExecutable:
		name = "file-is-executable"
	case PredPathIsSymbolicLink:
		name = "path-is-symbolic-link"
	case PredPathIsReadWrite:
		name = "path-is-read-write"
	case PredFirmware:
		name = "firmware"
	case PredMachineTag:
		name = "machine-tag"
	case PredCredential:
		name = "credential"
	case PredControlGroupController:
		name = "control-group-controller"
	default:
		name = fmt.Sprintf("kind-%d", p.Kind)
	}
	param := p.Param
	if p.Negate {
		param = "!" + param
	}
	return prefix + name + "=" + param
}

// Evaluate runs the predicate and returns ok plus a human-readable
// reason when ok is false. Negation is applied last — a failing
// underlying check on a negated predicate is OK; a succeeding check on
// a negated predicate is the failure path.
func (p Predicate) Evaluate() (bool, string) {
	raw, reason := evalRaw(p)
	if p.Negate {
		if raw {
			return false, fmt.Sprintf("%s is true (negated)", p.String())
		}
		return true, ""
	}
	if !raw {
		return false, reason
	}
	return true, ""
}

func evalRaw(p Predicate) (bool, string) {
	switch p.Kind {
	case PredPathExists:
		if _, err := os.Stat(p.Param); err != nil {
			return false, fmt.Sprintf("path %q does not exist", p.Param)
		}
		return true, ""
	case PredPathExistsGlob:
		matches, err := filepath.Glob(p.Param)
		if err != nil {
			return false, fmt.Sprintf("invalid glob %q: %v", p.Param, err)
		}
		if len(matches) == 0 {
			return false, fmt.Sprintf("glob %q matched nothing", p.Param)
		}
		return true, ""
	case PredPathIsDirectory:
		st, err := os.Stat(p.Param)
		if err != nil || !st.IsDir() {
			return false, fmt.Sprintf("%q is not a directory", p.Param)
		}
		return true, ""
	case PredPathIsMountPoint:
		ok, why := pathIsMountPoint(p.Param)
		return ok, why
	case PredFileNotEmpty:
		st, err := os.Stat(p.Param)
		if err != nil {
			return false, fmt.Sprintf("file %q: %v", p.Param, err)
		}
		if st.IsDir() {
			return false, fmt.Sprintf("%q is a directory", p.Param)
		}
		if st.Size() == 0 {
			return false, fmt.Sprintf("file %q is empty", p.Param)
		}
		return true, ""
	case PredDirectoryNotEmpty:
		entries, err := os.ReadDir(p.Param)
		if err != nil {
			return false, fmt.Sprintf("dir %q: %v", p.Param, err)
		}
		if len(entries) == 0 {
			return false, fmt.Sprintf("dir %q is empty", p.Param)
		}
		return true, ""
	case PredKernelCommandLine:
		return kernelCmdlineContains(p.Param)
	case PredVirtualization:
		return checkVirtualization(p.Param)
	case PredFirstBoot:
		return checkFirstBoot(p.Param)
	case PredHost:
		return checkHostMatch(p.Param)
	case PredSecurity:
		return checkSecurity(p.Param)
	case PredNeedsUpdate:
		return checkNeedsUpdate(p.Param)
	case PredACPower:
		return checkACPower(p.Param)
	case PredPathIsSocket:
		return pathIsSocket(p.Param)
	case PredFraction:
		return checkFraction(p.Param)
	case PredArchitecture:
		return checkArchitecture(p.Param)
	case PredCPUFeature:
		return checkCPUFeature(p.Param)
	case PredCPUs:
		return checkCPUs(p.Param)
	case PredMemory:
		return checkMemory(p.Param)
	case PredKernelVersion:
		return checkKernelVersion(p.Param)
	case PredKernelModuleLoaded:
		return checkKernelModuleLoaded(p.Param)
	case PredOSRelease:
		return checkOSRelease(p.Param)
	case PredUser:
		return checkUser(p.Param)
	case PredGroup:
		return checkGroup(p.Param)
	case PredEnvironment:
		return checkEnvironment(p.Param)
	case PredFileIsExecutable:
		return checkFileIsExecutable(p.Param)
	case PredPathIsSymbolicLink:
		return checkPathIsSymbolicLink(p.Param)
	case PredPathIsReadWrite:
		return checkPathIsReadWrite(p.Param)
	case PredFirmware:
		return checkFirmware(p.Param)
	case PredMachineTag:
		return checkMachineTag(p.Param)
	case PredCredential:
		return checkCredential(p.Param)
	case PredControlGroupController:
		return checkControlGroupController(p.Param)
	}
	return false, fmt.Sprintf("unknown predicate kind %d", p.Kind)
}

// pathIsSocket returns true iff path exists and is a Unix domain
// socket (matches systemd's ConditionPathIsSocket=). A path that
// exists but is not a socket, or that is missing entirely, fails.
func pathIsSocket(path string) (bool, string) {
	st, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf("path %q: %v", path, err)
	}
	if st.Mode()&os.ModeSocket == 0 {
		return false, fmt.Sprintf("%q is not a socket", path)
	}
	return true, ""
}

// checkFraction implements systemd's ConditionFraction=. Value shape
// is "<tag>:<percent>" — the tag is hashed together with the host's
// machine-id via FNV-1a to derive a stable 32-bit value; the condition
// succeeds iff that value modulo 100 < percent. Percent accepts an
// integer or one decimal place (0.5% granularity is enough for staged
// rollouts). The tag lets multiple independent rollouts on the same
// host resolve to independent bucket assignments.
//
// A missing /etc/machine-id fails the condition (opt-in only on hosts
// with a stable identifier) rather than falling back to hostname —
// that would make the roll-out non-stable across renames.
func checkFraction(param string) (bool, string) {
	spec := strings.TrimSpace(param)
	tag, pctStr, ok := strings.Cut(spec, ":")
	if !ok {
		return false, fmt.Sprintf("fraction: expected TAG:PERCENT, got %q", spec)
	}
	tag = strings.TrimSpace(tag)
	pctStr = strings.TrimSpace(pctStr)
	pct, err := strconv.ParseFloat(pctStr, 64)
	if err != nil {
		return false, fmt.Sprintf("fraction: percent %q: %v", pctStr, err)
	}
	if pct < 0 || pct > 100 {
		return false, fmt.Sprintf("fraction: percent %v out of [0,100]", pct)
	}
	mid, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return false, fmt.Sprintf("fraction: /etc/machine-id: %v", err)
	}
	h := fnv.New32a()
	h.Write([]byte(strings.TrimSpace(string(mid))))
	h.Write([]byte{0}) // separator so "abcTAG" and "abc"+"TAG" differ
	h.Write([]byte(tag))
	bucket := float64(h.Sum32()%10000) / 100.0 // two-decimal precision
	if bucket < pct {
		return true, ""
	}
	return false, fmt.Sprintf("fraction: bucket %.2f%% >= %.2f%%", bucket, pct)
}

// parsePredicateParam strips a leading "!" and reports whether the value
// is negated. Surrounding whitespace is removed first so users can write
// "! virtualization" or "!virtualization" indifferently.
func parsePredicateParam(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "!") {
		return strings.TrimSpace(v[1:]), true
	}
	return v, false
}

// PredicateKindByName maps the kebab-case identifier used in service
// descriptions to the internal PredicateKind enum. Returns false when
// the name is unknown — the parser uses this to validate.
func PredicateKindByName(name string) (PredicateKind, bool) {
	switch name {
	case "path-exists":
		return PredPathExists, true
	case "path-exists-glob":
		return PredPathExistsGlob, true
	case "path-is-directory":
		return PredPathIsDirectory, true
	case "path-is-mount-point":
		return PredPathIsMountPoint, true
	case "file-not-empty":
		return PredFileNotEmpty, true
	case "directory-not-empty":
		return PredDirectoryNotEmpty, true
	case "kernel-command-line":
		return PredKernelCommandLine, true
	case "virtualization":
		return PredVirtualization, true
	case "first-boot":
		return PredFirstBoot, true
	case "host":
		return PredHost, true
	case "security":
		return PredSecurity, true
	case "needs-update":
		return PredNeedsUpdate, true
	case "ac-power":
		return PredACPower, true
	case "path-is-socket":
		return PredPathIsSocket, true
	case "fraction":
		return PredFraction, true
	case "architecture":
		return PredArchitecture, true
	case "cpu-feature":
		return PredCPUFeature, true
	case "cpus":
		return PredCPUs, true
	case "memory":
		return PredMemory, true
	case "kernel-version":
		return PredKernelVersion, true
	case "kernel-module-loaded":
		return PredKernelModuleLoaded, true
	case "os-release":
		return PredOSRelease, true
	case "user":
		return PredUser, true
	case "group":
		return PredGroup, true
	case "environment":
		return PredEnvironment, true
	case "file-is-executable":
		return PredFileIsExecutable, true
	case "path-is-symbolic-link":
		return PredPathIsSymbolicLink, true
	case "path-is-read-write":
		return PredPathIsReadWrite, true
	case "firmware":
		return PredFirmware, true
	case "machine-tag":
		return PredMachineTag, true
	case "credential":
		return PredCredential, true
	case "control-group-controller":
		return PredControlGroupController, true
	}
	return 0, false
}
