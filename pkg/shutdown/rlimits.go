package shutdown

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// Linux-specific RLIMIT_* constants not exported by Go's syscall package.
// Values taken from include/uapi/asm-generic/resource.h. Keeping them local
// avoids pulling golang.org/x/sys/unix into the shutdown package just for
// a handful of integer constants.
const (
	rlimitRSS        = 5
	rlimitNPROC      = 6
	rlimitMEMLOCK    = 8
	rlimitLOCKS      = 10
	rlimitSIGPENDING = 11
	rlimitMSGQUEUE   = 12
	rlimitNICE       = 13
	rlimitRTPRIO     = 14
	rlimitRTTIME     = 15
)

// rlimitNames maps lowercase rlimit names (without the RLIMIT_ prefix) to
// their numeric resource IDs. Names match the conventions used by systemd
// and slinit's per-service rlimit-* settings, so that users can translate
// their knowledge between scopes.
var rlimitNames = map[string]int{
	"as":         syscall.RLIMIT_AS,
	"addrspace":  syscall.RLIMIT_AS,
	"core":       syscall.RLIMIT_CORE,
	"cpu":        syscall.RLIMIT_CPU,
	"data":       syscall.RLIMIT_DATA,
	"fsize":      syscall.RLIMIT_FSIZE,
	"nofile":     syscall.RLIMIT_NOFILE,
	"stack":      syscall.RLIMIT_STACK,
	"rss":        rlimitRSS,
	"nproc":      rlimitNPROC,
	"memlock":    rlimitMEMLOCK,
	"locks":      rlimitLOCKS,
	"sigpending": rlimitSIGPENDING,
	"msgqueue":   rlimitMSGQUEUE,
	"nice":       rlimitNICE,
	"rtprio":     rlimitRTPRIO,
	"rttime":     rlimitRTTIME,
}

// RlimUnlimited is the sentinel value used for "unlimited" — matches
// RLIM_INFINITY in Linux's resource.h.
const RlimUnlimited = ^uint64(0)

// BootRlimit represents a single global rlimit to apply at boot time.
type BootRlimit struct {
	Name     string // lowercase, as supplied by the user
	Resource int    // syscall.RLIMIT_* value
	Soft     uint64
	Hard     uint64
}

// setrlimitFunc is the syscall used to apply limits. Overridable for tests.
var setrlimitFunc = syscall.Setrlimit

// ParseBootRlimits parses a comma-separated list of "name=value" pairs
// where value is either "N", "soft:hard", or "unlimited". Whitespace
// around entries and names is tolerated. Unknown names and malformed
// values return a descriptive error. An empty input returns (nil, nil).
//
// Example:
//
//	"nofile=65536, core=unlimited, stack=8388608:unlimited"
func ParseBootRlimits(spec string) ([]BootRlimit, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}

	var out []BootRlimit
	seen := make(map[int]bool)

	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			return nil, fmt.Errorf("rlimit %q: expected name=value", part)
		}
		name := strings.ToLower(strings.TrimSpace(part[:eq]))
		value := strings.TrimSpace(part[eq+1:])

		// Accept rlimit-nofile/RLIMIT_NOFILE style for ergonomics.
		name = strings.TrimPrefix(name, "rlimit-")
		name = strings.TrimPrefix(name, "rlimit_")

		resource, ok := rlimitNames[name]
		if !ok {
			return nil, fmt.Errorf("rlimit %q: unknown resource name", name)
		}
		if seen[resource] {
			return nil, fmt.Errorf("rlimit %q: duplicate resource", name)
		}
		seen[resource] = true

		soft, hard, err := parseRlimitValue(value)
		if err != nil {
			return nil, fmt.Errorf("rlimit %q: %w", name, err)
		}
		if hard != RlimUnlimited && soft != RlimUnlimited && soft > hard {
			return nil, fmt.Errorf("rlimit %q: soft (%d) exceeds hard (%d)", name, soft, hard)
		}

		out = append(out, BootRlimit{Name: name, Resource: resource, Soft: soft, Hard: hard})
	}

	// Stable order (by resource number) keeps log output deterministic.
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

// parseRlimitValue accepts "N", "soft:hard", or "unlimited".
func parseRlimitValue(v string) (soft, hard uint64, err error) {
	parseOne := func(s string) (uint64, error) {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, fmt.Errorf("empty value")
		}
		if strings.EqualFold(s, "unlimited") || strings.EqualFold(s, "infinity") {
			return RlimUnlimited, nil
		}
		n, perr := strconv.ParseUint(s, 10, 64)
		if perr != nil {
			return 0, fmt.Errorf("invalid number %q", s)
		}
		return n, nil
	}

	if idx := strings.IndexByte(v, ':'); idx >= 0 {
		s, sErr := parseOne(v[:idx])
		if sErr != nil {
			return 0, 0, sErr
		}
		h, hErr := parseOne(v[idx+1:])
		if hErr != nil {
			return 0, 0, hErr
		}
		return s, h, nil
	}
	n, pErr := parseOne(v)
	if pErr != nil {
		return 0, 0, pErr
	}
	return n, n, nil
}

// ApplyBootRlimits applies a list of global resource limits to the
// current process (PID 1 / the slinit daemon). Limits are inherited by
// every child the daemon subsequently forks, so this is how a user sets
// system-wide defaults that apply to every service.
//
// Failures for individual limits are logged and the remainder continues;
// a single bad limit must not prevent boot. Returns the number of limits
// that were successfully applied.
func ApplyBootRlimits(limits []BootRlimit, logger *logging.Logger) int {
	applied := 0
	for _, l := range limits {
		lim := syscall.Rlimit{Cur: l.Soft, Max: l.Hard}
		if err := setrlimitFunc(l.Resource, &lim); err != nil {
			if logger != nil {
				logger.Error("Failed to set rlimit %s to %s:%s: %v",
					l.Name, formatRlimValue(l.Soft), formatRlimValue(l.Hard), err)
			}
			continue
		}
		applied++
		if logger != nil {
			logger.Debug("Boot rlimit %s = %s:%s",
				l.Name, formatRlimValue(l.Soft), formatRlimValue(l.Hard))
		}
	}
	return applied
}

// formatRlimValue renders a soft/hard value with "unlimited" as the
// sentinel instead of the raw 0xFFFFFFFFFFFFFFFF.
func formatRlimValue(v uint64) string {
	if v == RlimUnlimited {
		return "unlimited"
	}
	return strconv.FormatUint(v, 10)
}
