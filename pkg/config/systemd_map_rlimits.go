package config

import (
	"fmt"
	"strconv"
	"strings"
)

// The Limit*= directives that map onto the rlimit-* settings added in
// 4.1 (rlimitResources). LimitNOFILE/CORE/DATA/AS/MEMLOCK keep their
// bespoke cases in applyServiceKey.
//
// Each side of "soft:hard" is parsed as systemd's rlimit_parse_one does
// (src/basic/rlimit-util.c), per resource: a count, a size with base-1024
// suffixes, a time span in seconds (CPU, rounded up) or microseconds
// (RTTIME, where a bare number is microseconds), or a nice level (NICE).
// "infinity" is unlimited everywhere. An empty assignment unsets the
// limit, as in systemd.

type rlimitParse func(string) (uint64, error)

var sdRlimits = []struct {
	sd, slinit string
	parse      rlimitParse
}{
	{"LimitNPROC", "rlimit-nproc", rlimitCount},
	{"LimitLOCKS", "rlimit-locks", rlimitCount},
	{"LimitSIGPENDING", "rlimit-sigpending", rlimitCount},
	{"LimitRTPRIO", "rlimit-rtprio", rlimitCount},
	{"LimitFSIZE", "rlimit-fsize", rlimitSize},
	{"LimitSTACK", "rlimit-stack", rlimitSize},
	{"LimitRSS", "rlimit-rss", rlimitSize},
	{"LimitMSGQUEUE", "rlimit-msgqueue", rlimitSize},
	{"LimitCPU", "rlimit-cpu", rlimitSeconds},
	{"LimitRTTIME", "rlimit-rttime", rlimitUsec},
	{"LimitNICE", "rlimit-nice", rlimitNice},
}

func init() {
	for _, r := range sdRlimits {
		r := r
		registerService(r.sd, sdRule{custom: func(cfg *SystemdConfig, key, val string) []SystemdWarning {
			if strings.TrimSpace(trimQuotes(val)) == "" {
				cfg.dropMapped(r.slinit)
				return nil
			}
			return cfg.applyRule("Service", key, val, sdRule{key: r.slinit, conv: rlimitConv(r.parse)})
		}})
	}
}

// rlimitConv turns "soft[:hard]" into slinit's spec.
func rlimitConv(parse rlimitParse) func(string) (string, []SystemdWarning, error) {
	return func(v string) (string, []SystemdWarning, error) {
		soft, hard, two := strings.Cut(v, ":")
		s, err := rlimitSide(soft, parse)
		if err != nil {
			return "", nil, err
		}
		if !two {
			return s, nil, nil
		}
		h, err := rlimitSide(hard, parse)
		if err != nil {
			return "", nil, err
		}
		return s + ":" + h, nil, nil
	}
}

func rlimitSide(v string, parse rlimitParse) (string, error) {
	v = strings.TrimSpace(v)
	if v == "infinity" {
		return "unlimited", nil
	}
	n, err := parse(v)
	if err != nil {
		return "", err
	}
	if n == ^uint64(0) {
		return "unlimited", nil
	}
	return strconv.FormatUint(n, 10), nil
}

func rlimitCount(v string) (uint64, error) {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == ^uint64(0) {
		return 0, fmt.Errorf("%q is not a count", v)
	}
	return n, nil
}

func rlimitSize(v string) (uint64, error) {
	n, err := sdParseSize(v)
	if err != nil || n == ^uint64(0) {
		return 0, fmt.Errorf("%q is not a size", v)
	}
	return n, nil
}

// rlimitSeconds is rlimit_parse_sec: a time span, seconds by default,
// rounded up to whole seconds.
func rlimitSeconds(v string) (uint64, error) {
	us, ok := sdTimespanUsec(v)
	if !ok {
		return 0, fmt.Errorf("%q is not a time span", v)
	}
	return (us + 999999) / 1000000, nil
}

// rlimitUsec is rlimit_parse_usec: a time span whose default unit is the
// microsecond, so a bare number is microseconds.
func rlimitUsec(v string) (uint64, error) {
	if n, err := strconv.ParseUint(v, 10, 64); err == nil {
		return n, nil
	}
	us, ok := sdTimespanUsec(v)
	if !ok {
		return 0, fmt.Errorf("%q is not a time span", v)
	}
	return us, nil
}

// rlimitNice is rlimit_parse_nice: "+N" and "-N" are nice levels (the
// limit is 20 minus the level), an unsigned number the raw 0..40 value.
func rlimitNice(v string) (uint64, error) {
	bad := fmt.Errorf("%q is not a nice level (-20..19) or a raw limit (0..40)", v)
	switch {
	case strings.HasPrefix(v, "+"):
		n, err := strconv.ParseUint(v[1:], 10, 64)
		if err != nil || n >= 20 {
			return 0, bad
		}
		return 20 - n, nil
	case strings.HasPrefix(v, "-"):
		n, err := strconv.ParseUint(v[1:], 10, 64)
		if err != nil || n > 20 {
			return 0, bad
		}
		return 20 + n, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n > 40 {
		return 0, bad
	}
	return n, nil
}
