package rng

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

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

// recordFeeds stubs the kernel feed and records every call. The stub
// enforces the real RNDADDENTROPY size limit so a too-long seed shows
// up as an error, as it would on a real kernel.
type feedCall struct {
	data   []byte
	credit bool
}

func recordFeeds(t *testing.T) *[]feedCall {
	t.Helper()
	calls := &[]feedCall{}
	withStubsCustom(t, func(b []byte, credit bool) error {
		*calls = append(*calls, feedCall{data: append([]byte(nil), b...), credit: credit})
		if len(b) > maxSeedLen {
			return errors.New("seed too big")
		}
		return nil
	}, defaultStubGetrandom)
	return calls
}

// TestRun_SymlinkNoCreditNotFollowed: a symlink planted at
// seed.no-credit pointing outside the seed dir must not make root
// create (or truncate) the target when the new seed is written.
func TestRun_SymlinkNoCreditNotFollowed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "victim")
	if err := os.Symlink(target, filepath.Join(dir, nonCreditableSeed)); err != nil {
		t.Fatal(err)
	}
	recordFeeds(t)

	_, _ = Run(Options{SeedDir: dir})

	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target outside seed dir was created: lstat err=%v", err)
	}
	for _, n := range []string{nonCreditableSeed, creditableSeed} {
		fi, err := os.Lstat(filepath.Join(dir, n))
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s is still a symlink after the cycle", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, creditableSeed)); err != nil {
		t.Fatalf("fresh seed not written: %v", err)
	}
}

// TestRun_SymlinkSeedNotCredited: a seed.credit symlink to an existing
// file must neither have the file's bytes fed (let alone credited) nor
// the file itself modified.
func TestRun_SymlinkSeedNotCredited(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "chosen")
	payload := bytes.Repeat([]byte("attacker-chosen!"), 4)
	if err := os.WriteFile(target, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{creditableSeed, nonCreditableSeed} {
		if err := os.Symlink(target, filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	calls := recordFeeds(t)

	res, err := Run(Options{SeedDir: dir})
	if err == nil {
		t.Fatal("Run should report the rejected symlink seeds")
	}
	if res.CreditableConsumed || res.NonCreditableConsumed {
		t.Fatalf("symlink seed reported as consumed: %+v", res)
	}
	for _, c := range *calls {
		if bytes.Equal(c.data, payload) {
			t.Fatalf("symlink target content fed to the kernel (credit=%v)", c.credit)
		}
	}
	got, rerr := os.ReadFile(target)
	if rerr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("symlink target modified: %q err=%v", got, rerr)
	}
	for _, n := range []string{nonCreditableSeed, creditableSeed} {
		fi, err := os.Lstat(filepath.Join(dir, n))
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s symlink was not removed", n)
		}
	}
}

// TestRun_FIFODoesNotBlock: a FIFO at seed.credit must be rejected
// without blocking the cycle (which holds the dir lock meanwhile).
func TestRun_FIFODoesNotBlock(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, creditableSeed)
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	calls := recordFeeds(t)

	done := make(chan error, 1)
	go func() {
		_, err := Run(Options{SeedDir: dir})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run should report the rejected FIFO seed")
		}
	case <-time.After(5 * time.Second):
		// Unblock the leaked open so the test binary can exit.
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		t.Fatal("Run blocked on a FIFO seed")
	}
	if len(*calls) != 0 {
		t.Fatalf("FIFO seed was fed to the kernel: %d calls", len(*calls))
	}
	fi, err := os.Lstat(fifo)
	if err != nil {
		t.Fatalf("fresh seed missing: %v", err)
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("seed.credit is still a %v", fi.Mode().Type())
	}
}

// TestRun_OversizeSeedTruncated: a seed longer than maxSeedLen is fed
// as its first maxSeedLen bytes instead of being unlinked and lost.
func TestRun_OversizeSeedTruncated(t *testing.T) {
	dir := t.TempDir()
	seed := make([]byte, 600)
	for i := range seed {
		seed[i] = byte(i * 13)
	}
	if err := os.WriteFile(filepath.Join(dir, creditableSeed), seed, 0o400); err != nil {
		t.Fatal(err)
	}
	calls := recordFeeds(t)

	res, err := Run(Options{SeedDir: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.CreditableConsumed {
		t.Fatal("oversize seed not consumed")
	}
	if len(*calls) != 1 {
		t.Fatalf("want 1 feed, got %d", len(*calls))
	}
	c := (*calls)[0]
	if !bytes.Equal(c.data, seed[:maxSeedLen]) || !c.credit {
		t.Fatalf("fed %d bytes (credit=%v), want first %d bytes credited", len(c.data), c.credit, maxSeedLen)
	}
}

// TestRun_InsecureDirRefused: a group/world-writable seed dir must be
// refused before anything in it is consumed or written.
func TestRun_InsecureDirRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(dir, creditableSeed)
	planted := bytes.Repeat([]byte{0xCD}, 64)
	if err := os.WriteFile(seedPath, planted, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := recordFeeds(t)

	_, err := Run(Options{SeedDir: dir})
	if !errors.Is(err, errInsecureSeedDir) {
		t.Fatalf("Run on a 0777 dir: err=%v, want errInsecureSeedDir", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("seed from insecure dir was fed: %d calls", len(*calls))
	}
	if got, _ := os.ReadFile(seedPath); !bytes.Equal(got, planted) {
		t.Fatal("seed in insecure dir was touched")
	}
}

// TestCheckSeedDir_Owner: a dir owned by someone else is refused.
func TestCheckSeedDir_Owner(t *testing.T) {
	dir := t.TempDir()
	dfd, err := unix.Open(dir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dfd)
	if err := checkSeedDir(dfd, os.Geteuid()); err != nil {
		t.Fatalf("own 0700 dir refused: %v", err)
	}
	if err := checkSeedDir(dfd, os.Geteuid()+1); !errors.Is(err, errInsecureSeedDir) {
		t.Fatalf("foreign-owned dir accepted: err=%v", err)
	}
}
