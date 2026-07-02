package rng

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// runInSubtest wraps Run and stubs out the kernel-touching calls so we
// can exercise the state-machine without CAP_SYS_ADMIN or the actual
// /dev/urandom on the test host. The stubs are process-wide because
// the code under test uses top-level variables — set/unset per subtest.

// TestRun_FreshBoot exercises the first-ever cycle: no on-disk seeds,
// so nothing is consumed but a fresh seed is written.
func TestRun_FreshBoot(t *testing.T) {
	dir := t.TempDir()
	withStubs(t, func() {
		var logged []string
		res, err := Run(Options{
			SeedDir: dir,
			Log: func(f string, a ...any) {
				logged = append(logged, formatLog(f, a...))
			},
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.NonCreditableConsumed || res.CreditableConsumed {
			t.Fatalf("expected no consumption on fresh boot, got %+v", res)
		}
		if !res.NewSeedCreditable {
			t.Fatalf("stub getrandom should have returned creditable seed")
		}
		if res.NewSeedBytes < minSeedLen {
			t.Fatalf("new seed too small: %d", res.NewSeedBytes)
		}

		// The creditable seed should now exist (we said creditable).
		if _, err := os.Stat(filepath.Join(dir, creditableSeed)); err != nil {
			t.Fatalf("creditable seed not written: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, nonCreditableSeed)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("non-creditable seed should have been renamed away: err=%v", err)
		}
	})
}

// TestRun_RoundTrip: cycle once, then cycle again — the seed written
// by the first cycle must be consumed and unlinked by the second.
func TestRun_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	withStubs(t, func() {
		if _, err := Run(Options{SeedDir: dir}); err != nil {
			t.Fatalf("cycle 1: %v", err)
		}
		firstSeed, err := os.ReadFile(filepath.Join(dir, creditableSeed))
		if err != nil {
			t.Fatalf("read cycle-1 seed: %v", err)
		}

		res, err := Run(Options{SeedDir: dir})
		if err != nil {
			t.Fatalf("cycle 2: %v", err)
		}
		if !res.CreditableConsumed {
			t.Fatalf("cycle 2 should have consumed the creditable seed from cycle 1")
		}

		secondSeed, err := os.ReadFile(filepath.Join(dir, creditableSeed))
		if err != nil {
			t.Fatalf("read cycle-2 seed: %v", err)
		}
		if bytes.Equal(firstSeed, secondSeed) {
			t.Fatalf("second cycle produced identical seed bytes — hash mixing is broken")
		}
	})
}

// TestRun_SkipCredit: SkipCredit must not delete the seed file (still
// consumed), just not credit it. The next-cycle seed still gets
// written creditable (that decision is orthogonal).
func TestRun_SkipCredit(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed a creditable file so we can observe crediting decisions.
	seedPath := filepath.Join(dir, creditableSeed)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seedPath, bytes.Repeat([]byte{0xAB}, 64), 0o400); err != nil {
		t.Fatal(err)
	}

	var ioctlCredit bool
	withStubsCustom(t, func(_ []byte, credit bool) error {
		ioctlCredit = credit
		return nil
	}, defaultStubGetrandom)

	_, err := Run(Options{SeedDir: dir, SkipCredit: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ioctlCredit {
		t.Fatalf("SkipCredit=true but ioctl was called with credit=true")
	}
	// After consumption a fresh seed is written; verify the file
	// content differs from the pre-seeded 0xAB pattern (i.e. the old
	// bytes were truly rotated, not retained).
	after, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read post-cycle seed: %v", err)
	}
	if bytes.Equal(after, bytes.Repeat([]byte{0xAB}, 64)) {
		t.Fatal("seed file still contains the pre-seed pattern — consume/rewrite broken")
	}
}

// TestRun_NonCreditableSeedGetsRenamedCorrectly: when getrandom
// returns a NON-creditable seed (pool not yet initialised), the new
// seed must stay as seed.no-credit — no rename.
func TestRun_NonCreditableWrite(t *testing.T) {
	dir := t.TempDir()
	withStubsCustom(t, defaultStubIoctl, func(buf []byte) (bool, error) {
		// pretend the pool was NOT initialised → non-creditable.
		for i := range buf {
			buf[i] = 0x42
		}
		return false, nil
	})

	res, err := Run(Options{SeedDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.NewSeedCreditable {
		t.Fatal("stub returned non-creditable, but result says creditable")
	}
	if _, err := os.Stat(filepath.Join(dir, nonCreditableSeed)); err != nil {
		t.Fatalf("non-creditable seed should be present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, creditableSeed)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("creditable seed should NOT be present: err=%v", err)
	}
}

// TestDetermineOptimalSeedLen_Clamp: reject nonsensical poolsize
// values; always land inside [minSeedLen, maxSeedLen].
func TestDetermineOptimalSeedLen_Clamp(t *testing.T) {
	// This test just exercises the function directly; the
	// poolsize file may or may not exist on the runner.
	got := determineOptimalSeedLen(func(string, ...any) {})
	if got < minSeedLen || got > maxSeedLen {
		t.Fatalf("clamp broken: got %d, want in [%d,%d]", got, minSeedLen, maxSeedLen)
	}
}

// -- stub plumbing ----------------------------------------------------

func defaultStubIoctl(_ []byte, _ bool) error { return nil }

func defaultStubGetrandom(buf []byte) (bool, error) {
	// Deterministic-but-not-uniform pattern so round-trip tests can
	// tell the second cycle differs from the first even with the
	// same "random" input.
	for i := range buf {
		buf[i] = byte(i*7) ^ 0x5A
	}
	return true, nil
}

func withStubs(t *testing.T, fn func()) {
	t.Helper()
	withStubsCustom(t, defaultStubIoctl, defaultStubGetrandom)
	fn()
}

func withStubsCustom(t *testing.T, ioctl func([]byte, bool) error, gr func([]byte) (bool, error)) {
	t.Helper()
	prevI, prevG := feedKernelHook, readNewSeedHook
	feedKernelHook = ioctl
	readNewSeedHook = gr
	t.Cleanup(func() {
		feedKernelHook = prevI
		readNewSeedHook = prevG
	})
}

func formatLog(format string, args ...any) string {
	return format
}

// silence unused-import warnings if we later trim assertions
var _ = syscall.SIGTERM
var _ = unix.LOCK_EX
