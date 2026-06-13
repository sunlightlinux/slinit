package service

import (
	"fmt"
	"os"
	"path/filepath"
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
	}
	return false, fmt.Sprintf("unknown predicate kind %d", p.Kind)
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
	}
	return 0, false
}
