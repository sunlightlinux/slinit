package dissect

import (
	"fmt"
	"strings"
)

// Policy is the parsed form of `journalctl --image-policy=…`. Slinit
// interprets only a small subset of systemd's full policy syntax:
// the coarse `Strict` boolean is enough to decide whether to error
// on encrypted / LVM / verity partitions or silently skip them.
//
// Full syntax (per systemd docs):
//
//	root=verity+encrypted+signed:usr=verity+signed:home=encrypted
//
// slinit maps:
//
//	""            → loose default (skip unsupported, keep looking)
//	"strict"      → refuse encrypted / LVM / verity outright
//	"loose"       → same as ""
//	full-syntax   → parsed, Strict=false, PerPartition preserved for
//	                future use; unsupported constraints ignored
type Policy struct {
	Strict bool
	// PerPartition captures the full parsed policy tokens keyed by
	// partition name (root, usr, home, srv, tmp, var, esp, xbootldr).
	// Currently informational — Attach doesn't consult it. Reserved
	// so future LUKS-aware slinit versions can honor per-partition
	// rules without a wire change.
	PerPartition map[string][]string
}

// ParsePolicy accepts the shorthand keywords + systemd's full
// colon-separated per-partition form. Empty string is the loose
// default. Invalid syntax returns an error naming the offender.
func ParsePolicy(s string) (*Policy, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "loose" || s == "default" {
		return &Policy{}, nil
	}
	if s == "strict" {
		return &Policy{Strict: true}, nil
	}
	// Full form: token1:token2:...
	p := &Policy{PerPartition: map[string][]string{}}
	for _, seg := range strings.Split(s, ":") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		eq := strings.IndexByte(seg, '=')
		if eq < 0 {
			return nil, fmt.Errorf("dissect: policy token %q missing `=`", seg)
		}
		name := strings.TrimSpace(seg[:eq])
		constraints := strings.Split(strings.TrimSpace(seg[eq+1:]), "+")
		for i, c := range constraints {
			constraints[i] = strings.TrimSpace(c)
		}
		p.PerPartition[name] = constraints
	}
	return p, nil
}
