package persist

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPinStoreDisabledIsNoop guards the empty-dir contract every call
// site relies on: with persist-intent unset the store must silently
// swallow every method call so hook sites don't need a "feature on?"
// branch.
func TestPinStoreDisabledIsNoop(t *testing.T) {
	p := NewPinStore("")
	if p.Enabled() {
		t.Fatal("empty-dir store reported Enabled()=true")
	}
	if err := p.Set("svc", IntentPinnedStarted); err != nil {
		t.Errorf("Set on disabled store returned err: %v", err)
	}
	if err := p.Clear("svc"); err != nil {
		t.Errorf("Clear on disabled store returned err: %v", err)
	}
	got, err := p.Load()
	if err != nil {
		t.Errorf("Load on disabled store returned err: %v", err)
	}
	if got != nil {
		t.Errorf("Load on disabled store returned %v, want nil", got)
	}
}

// TestPinStoreRoundTripStarted exercises the write→read cycle for a
// pinned-started intent. After Set, Load must surface the exact
// intent for that service name.
func TestPinStoreRoundTripStarted(t *testing.T) {
	dir := t.TempDir()
	p := NewPinStore(dir)

	if err := p.Set("web", IntentPinnedStarted); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := p.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["web"] != IntentPinnedStarted {
		t.Errorf("got[%q] = %q, want %q", "web", got["web"], IntentPinnedStarted)
	}
}

// TestPinStoreRoundTripStopped covers the parallel path for
// pinned-stopped, which is the more operator-visible case (a service
// held down across reboots).
func TestPinStoreRoundTripStopped(t *testing.T) {
	dir := t.TempDir()
	p := NewPinStore(dir)

	if err := p.Set("db", IntentPinnedStopped); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := p.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["db"] != IntentPinnedStopped {
		t.Errorf("got[%q] = %q, want %q", "db", got["db"], IntentPinnedStopped)
	}
}

// TestPinStoreClear guards the "unpin drops the persisted intent"
// invariant so a subsequent boot doesn't re-apply a pin the operator
// already removed.
func TestPinStoreClear(t *testing.T) {
	dir := t.TempDir()
	p := NewPinStore(dir)

	_ = p.Set("svc", IntentPinnedStopped)
	if err := p.Clear("svc"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	got, _ := p.Load()
	if _, ok := got["svc"]; ok {
		t.Errorf("Clear left intent behind: %v", got)
	}
	// Second Clear must be idempotent — otherwise `unpin` on a
	// never-pinned service would fail the caller with a spurious
	// "no such file" error.
	if err := p.Clear("svc"); err != nil {
		t.Errorf("second Clear: %v", err)
	}
}

// TestPinStoreRejectsInvalidIntent surfaces programming errors early
// rather than silently persisting a value Load can't parse back.
func TestPinStoreRejectsInvalidIntent(t *testing.T) {
	dir := t.TempDir()
	p := NewPinStore(dir)
	if err := p.Set("svc", "bogus"); err == nil {
		t.Fatal("Set with bogus intent should have errored")
	}
}

// TestPinStoreRejectsPathTraversal guards against a service name
// crafted to escape the store dir (e.g. a compromised config file).
// This isn't a full threat model — ValidateServiceName upstream is
// stricter — but the direct check keeps the store honest on its own.
func TestPinStoreRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	p := NewPinStore(dir)
	for _, bad := range []string{"", ".", "..", "svc/../etc/passwd", "svc\x00null"} {
		if err := p.Set(bad, IntentPinnedStopped); err == nil {
			t.Errorf("Set(%q) should have errored", bad)
		}
	}
}

// TestPinStoreLoadIgnoresCorruptFiles proves the load path is
// resilient: a corrupted file for one service must not gate the
// restore of every other service.
func TestPinStoreLoadIgnoresCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	p := NewPinStore(dir)

	// Good entry.
	if err := p.Set("good", IntentPinnedStopped); err != nil {
		t.Fatalf("seed good: %v", err)
	}
	// Corrupt entry — directly write an unknown value.
	if err := os.WriteFile(filepath.Join(dir, "bad"), []byte("garbage"), 0644); err != nil {
		t.Fatalf("seed bad: %v", err)
	}
	got, err := p.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["good"] != IntentPinnedStopped {
		t.Errorf("good service intent missing: %v", got)
	}
	if _, ok := got["bad"]; ok {
		t.Errorf("corrupt entry should have been skipped, got: %v", got["bad"])
	}
}
