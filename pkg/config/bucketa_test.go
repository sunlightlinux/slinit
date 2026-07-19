package config

import (
	"strings"
	"testing"
)

// TestParseBucketAHardening verifies every restrict-*/memory-deny-*
// directive round-trips through the parser into the corresponding
// ServiceDescription field. Purely a wiring test — actual BPF
// programs are covered by pkg/seccomp tests, and the runner-side
// prctl by the functional suite.
func TestParseBucketAHardening(t *testing.T) {
	input := `
type = process
command = /bin/true
restrict-realtime = yes
restrict-namespaces = yes
restrict-suidsgid = yes
restrict-file-systems = yes
memory-deny-write-execute = yes
restrict-address-families = AF_INET AF_UNIX AF_INET6
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.RestrictRealtime {
		t.Error("restrict-realtime not set")
	}
	if !desc.RestrictNamespaces {
		t.Error("restrict-namespaces not set")
	}
	if !desc.RestrictSUIDSGID {
		t.Error("restrict-suidsgid not set")
	}
	if !desc.RestrictFileSystems {
		t.Error("restrict-file-systems not set")
	}
	if !desc.MemoryDenyWriteExecute {
		t.Error("memory-deny-write-execute not set")
	}
	if !desc.RestrictAFEnabled {
		t.Error("restrict-address-families presence not recorded")
	}
	if len(desc.RestrictAddressFamilies) != 3 {
		t.Fatalf("restrict-address-families len=%d, want 3", len(desc.RestrictAddressFamilies))
	}
}

// TestParseRestrictAddressFamiliesEmpty: an empty value on the
// directive still records "enabled" — the operator asked for a
// deny-all sockets stance, which is the strictest interpretation
// available. Distinguishes from a totally-absent directive
// (RestrictAFEnabled false, and no filter installed at all).
func TestParseRestrictAddressFamiliesEmpty(t *testing.T) {
	input := `
type = process
command = /bin/true
restrict-address-families =
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !desc.RestrictAFEnabled {
		t.Errorf("empty allow-list must still flip Enabled")
	}
	if len(desc.RestrictAddressFamilies) != 0 {
		t.Errorf("empty value must not populate families: %v", desc.RestrictAddressFamilies)
	}
}

// TestParseRestrictAddressFamiliesAppend: += extends the allow-list
// across multiple lines, matching the `system-call-filter += @net`
// idiom operators already use for seccomp filters.
func TestParseRestrictAddressFamiliesAppend(t *testing.T) {
	input := `
type = process
command = /bin/true
restrict-address-families = AF_UNIX
restrict-address-families += AF_INET
restrict-address-families += AF_INET6
`
	desc, err := Parse(strings.NewReader(input), "svc", "test-file")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(desc.RestrictAddressFamilies) != 3 {
		t.Fatalf("want 3 families, got %d: %v",
			len(desc.RestrictAddressFamilies), desc.RestrictAddressFamilies)
	}
}
