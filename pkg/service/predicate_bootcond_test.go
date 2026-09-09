package service

import (
	"os"
	"path/filepath"
	"testing"
)

// withBootCmdline points bootCondActivePath at a test fixture with
// the given /proc/cmdline contents, and returns a restore func.
func withBootCmdline(t *testing.T, cmdline string) func() {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(p, []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := bootCondActivePath
	bootCondActivePath = p
	return func() { bootCondActivePath = orig }
}

// TestCheckBootCond_Match: a single-value slinit.cond= sets exactly
// that condition. Baseline happy path.
func TestCheckBootCond_Match(t *testing.T) {
	restore := withBootCmdline(t, "console=ttyS0 slinit.cond=factory root=/dev/sda1")
	defer restore()

	ok, reason := checkBootCond("factory")
	if !ok {
		t.Errorf("factory should match, got reason: %s", reason)
	}
}

// TestCheckBootCond_MultiValue: comma-separated list on a single
// slinit.cond= token — every listed value matches. This is the
// core finit contract (`slinit.cond=factory,upgrade` sets both).
func TestCheckBootCond_MultiValue(t *testing.T) {
	restore := withBootCmdline(t, "slinit.cond=factory,upgrade,verify")
	defer restore()

	for _, want := range []string{"factory", "upgrade", "verify"} {
		if ok, reason := checkBootCond(want); !ok {
			t.Errorf("%q should match in comma list, got reason: %s", want, reason)
		}
	}
	if ok, _ := checkBootCond("provisioning"); ok {
		t.Error("provisioning should NOT match (not in list)")
	}
}

// TestCheckBootCond_MultipleTokens: finit accepts multiple
// slinit.cond= tokens on the same cmdline; both merge into the
// active-list. Guards against a naive "only look at first hit"
// implementation.
func TestCheckBootCond_MultipleTokens(t *testing.T) {
	restore := withBootCmdline(t, "slinit.cond=factory slinit.cond=verify,upgrade")
	defer restore()

	for _, want := range []string{"factory", "verify", "upgrade"} {
		if ok, reason := checkBootCond(want); !ok {
			t.Errorf("%q should match across tokens, got reason: %s", want, reason)
		}
	}
}

// TestCheckBootCond_FinitParityToken: an operator with an
// unchanged finit boot cmdline should still trigger slinit's
// boot-cond machinery. finit.cond= is the parity shim.
func TestCheckBootCond_FinitParityToken(t *testing.T) {
	restore := withBootCmdline(t, "root=/dev/nvme0n1 finit.cond=factory")
	defer restore()

	if ok, reason := checkBootCond("factory"); !ok {
		t.Errorf("finit.cond=factory should match, got reason: %s", reason)
	}
}

// TestCheckBootCond_NoMatch: nothing in /proc/cmdline mentions
// the requested tag — clear failure with a human-readable reason.
func TestCheckBootCond_NoMatch(t *testing.T) {
	restore := withBootCmdline(t, "console=ttyS0 root=/dev/sda1")
	defer restore()

	ok, reason := checkBootCond("factory")
	if ok {
		t.Error("no slinit.cond= at all → factory must not match")
	}
	if reason == "" {
		t.Error("failure reason should be non-empty")
	}
}

// TestCheckBootCond_EmptyTag: a service configured with
// `condition-boot-cond=` (empty value) never matches — guards
// against a typo/misconfig silently pinning the service to
// "starts always".
func TestCheckBootCond_EmptyTag(t *testing.T) {
	restore := withBootCmdline(t, "slinit.cond=factory")
	defer restore()

	if ok, _ := checkBootCond(""); ok {
		t.Error("empty tag must not match anything")
	}
	if ok, _ := checkBootCond("   "); ok {
		t.Error("whitespace-only tag must not match anything")
	}
}

// TestCheckBootCond_MissingCmdline: /proc/cmdline unreadable →
// failure with the read error surfaced in the reason. Real
// initramfs boots hit this transiently before /proc is mounted.
func TestCheckBootCond_MissingCmdline(t *testing.T) {
	orig := bootCondActivePath
	bootCondActivePath = "/no/such/cmdline"
	defer func() { bootCondActivePath = orig }()

	if ok, _ := checkBootCond("factory"); ok {
		t.Error("missing cmdline should never match")
	}
}

// TestPredicateBootCond_ParseAndString: the config-level directive
// name `condition-boot-cond` round-trips through PredicateKindByName
// and back through String() — regression guard against a name-mapping
// drift the parser would silently swallow.
func TestPredicateBootCond_ParseAndString(t *testing.T) {
	kind, ok := PredicateKindByName("boot-cond")
	if !ok || kind != PredBootCond {
		t.Fatalf("PredicateKindByName(\"boot-cond\") = (%v, %v); want (PredBootCond, true)", kind, ok)
	}
	p := Predicate{Kind: kind, Param: "factory"}
	got := p.String()
	want := "condition-boot-cond=factory"
	if got != want {
		t.Errorf("Predicate.String() = %q, want %q", got, want)
	}
}
