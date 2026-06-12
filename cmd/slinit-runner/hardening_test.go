package main

import (
	"testing"
)

// TestCollectDenyListSpotChecks verifies each active knob contributes
// its expected syscalls to the deny list. A regression here would
// silently produce a weaker filter than the operator asked for.
func TestCollectDenyListSpotChecks(t *testing.T) {
	cases := []struct {
		name  string
		spec  hardeningSpec
		hasSC []string
	}{
		{"kernel-tunables", hardeningSpec{protectKernelTunables: true},
			[]string{"iopl", "ioperm", "swapon", "swapoff"}},
		{"kernel-modules", hardeningSpec{protectKernelModules: true},
			[]string{"init_module", "finit_module", "delete_module"}},
		{"kernel-logs", hardeningSpec{protectKernelLogs: true},
			[]string{"syslog"}},
		{"clock", hardeningSpec{protectClock: true},
			[]string{"clock_settime", "clock_adjtime", "settimeofday", "adjtimex"}},
		{"hostname", hardeningSpec{protectHostname: true},
			[]string{"sethostname", "setdomainname"}},
		{"personality", hardeningSpec{lockPersonality: true},
			[]string{"personality"}},
	}
	for _, c := range cases {
		got := collectDenyList(c.spec)
		set := make(map[string]struct{}, len(got))
		for _, s := range got {
			set[s] = struct{}{}
		}
		for _, want := range c.hasSC {
			if _, ok := set[want]; !ok {
				t.Errorf("%s: deny list missing %q (got %v)", c.name, want, got)
			}
		}
	}
}

// TestCollectDenyListEmpty verifies inactivity → empty list, so the
// runner can short-circuit the seccomp install path without churn.
func TestCollectDenyListEmpty(t *testing.T) {
	if got := collectDenyList(hardeningSpec{}); len(got) != 0 {
		t.Errorf("inactive spec should produce empty list, got %v", got)
	}
}

// TestHardeningSpecNeedsMountNS checks the helper used by both the
// loader (to auto-imply CLONE_NEWNS) and the runner (to call
// MS_PRIVATE only when needed).
func TestHardeningSpecNeedsMountNS(t *testing.T) {
	mountSpecs := []hardeningSpec{
		{protectKernelTunables: true},
		{protectControlGroups: true},
		{protectKernelLogs: true},
	}
	for _, s := range mountSpecs {
		if !s.needsMountNS() {
			t.Errorf("%+v should need mount NS", s)
		}
	}
	seccompOnly := []hardeningSpec{
		{protectClock: true},
		{protectHostname: true},
		{lockPersonality: true},
		{protectKernelModules: true},
	}
	for _, s := range seccompOnly {
		if s.needsMountNS() {
			t.Errorf("%+v should NOT need mount NS", s)
		}
	}
}
