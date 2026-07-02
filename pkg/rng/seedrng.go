// Package rng implements the SeedRNG protocol: persistently seed the
// kernel RNG across reboots by consuming an on-disk seed at boot and
// producing a fresh seed at shutdown (or on periodic re-runs).
//
// Ported from OpenRC's seedrng.c (which was itself derived from
// https://git.zx2c4.com/seedrng/ by Jason Donenfeld). The systemd
// equivalent is systemd-random-seed.
//
// Semantics preserved from the reference implementation:
//   - Consumes NON_CREDITABLE_SEED first, then CREDITABLE_SEED.
//   - Each consumed file is unlinked (with fsync of the dir) before its
//     bytes are fed to the kernel via RNDADDENTROPY, so a crash cannot
//     replay the same seed on the next boot.
//   - New seed = kernel getrandom() output, with its trailing 32 bytes
//     overwritten by SHA-256(prefix || realtime || boottime || old
//     seeds || fresh bytes). This binds the persisted seed to the
//     current wallclock and boottime so restoring a snapshot without
//     changing the clock still ends up with distinct RNG state.
//   - The written file is always non-creditable first, then rename()d
//     to creditable iff getrandom(GRND_NONBLOCK) succeeded — otherwise
//     the pool is not yet initialised and we must not claim entropy on
//     the next boot.
//
// Deviation from the C original: SHA-256 replaces BLAKE2s. Seed files
// are opaque byte bags — nothing reads the hash back out — so the
// choice of digest is an internal implementation detail; SHA-256 is in
// the Go stdlib and BLAKE2s would require an external dependency for
// zero functional gain.
package rng

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// DefaultSeedDir is where seed files live between boots.
	DefaultSeedDir = "/var/lib/seedrng"

	creditableSeed    = "seed.credit"
	nonCreditableSeed = "seed.no-credit"

	minSeedLen = 32  // SHA-256 output width — never persist fewer bytes.
	maxSeedLen = 512 // Ceiling matching the reference implementation.
	hashLen    = 32

	seedRNGPrefix  = "SeedRNG v1 Old+New Prefix"
	seedRNGFailure = "SeedRNG v1 No New Seed Failure"
)

// randPoolInfo mirrors struct rand_pool_info from <linux/random.h>.
// EntropyCount is measured in *bits*; BufSize is in bytes.
type randPoolInfo struct {
	EntropyCount int32
	BufSize      int32
	Buf          [maxSeedLen]byte
}

// Options controls a single seed cycle.
type Options struct {
	// SeedDir is the directory holding seed files. Defaults to DefaultSeedDir.
	SeedDir string
	// SkipCredit disables entropy crediting even if the incoming seed was
	// marked creditable. Useful in environments where an attacker might
	// have controlled the on-disk seed.
	SkipCredit bool
	// Log receives one line per notable event; nil disables logging.
	Log func(format string, args ...any)
}

// Result summarises what happened during a Run.
type Result struct {
	// NonCreditableConsumed is true if NON_CREDITABLE_SEED existed and
	// was fed to the kernel (without crediting).
	NonCreditableConsumed bool
	// CreditableConsumed is true if CREDITABLE_SEED existed and was fed
	// to the kernel. Whether crediting actually happened is governed by
	// SkipCredit.
	CreditableConsumed bool
	// NewSeedCreditable is true if the *new* seed just written should be
	// treated as creditable on the next boot (getrandom() gave real
	// entropy this time).
	NewSeedCreditable bool
	// NewSeedBytes is the length of the seed just written.
	NewSeedBytes int
}

// Run performs one seed cycle. Errors from the consume phase are
// collected but do not prevent a new seed from being written — the
// worst case is still better than leaving the RNG unseeded next boot.
// The joined error, if any, indicates any degraded outcomes.
func Run(opts Options) (Result, error) {
	if opts.SeedDir == "" {
		opts.SeedDir = DefaultSeedDir
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}

	oldMask := syscall.Umask(0o077)
	defer syscall.Umask(oldMask)

	if err := os.MkdirAll(opts.SeedDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create seed dir %s: %w", opts.SeedDir, err)
	}

	dfd, err := unix.Open(opts.SeedDir, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return Result{}, fmt.Errorf("open seed dir %s: %w", opts.SeedDir, err)
	}
	defer unix.Close(dfd)

	// Exclusive lock prevents a second seedrng invocation from consuming
	// the same seed file concurrently and double-crediting entropy.
	if err := unix.Flock(dfd, unix.LOCK_EX); err != nil {
		return Result{}, fmt.Errorf("lock seed dir: %w", err)
	}

	hash := sha256.New()
	hash.Write([]byte(seedRNGPrefix))
	realtime := timespecNow(unix.CLOCK_REALTIME)
	boottime := timespecNow(unix.CLOCK_BOOTTIME)
	writeTimespec(hash, realtime)
	writeTimespec(hash, boottime)

	var res Result
	var errs []error

	// Non-creditable first: matches OpenRC/systemd ordering.
	consumed, err := consumeSeed(opts.SeedDir, dfd, nonCreditableSeed, false, hash, opts.Log)
	if err != nil {
		errs = append(errs, fmt.Errorf("consume non-creditable: %w", err))
	}
	res.NonCreditableConsumed = consumed

	credit := !opts.SkipCredit
	consumed, err = consumeSeed(opts.SeedDir, dfd, creditableSeed, credit, hash, opts.Log)
	if err != nil {
		errs = append(errs, fmt.Errorf("consume creditable: %w", err))
	}
	res.CreditableConsumed = consumed

	newSeedLen := determineOptimalSeedLen(opts.Log)
	newSeed := make([]byte, newSeedLen)
	creditable, err := readNewSeedHook(newSeed)
	if err != nil {
		opts.Log("Unable to read new seed: %v", err)
		// Fall back to a deterministic marker so we still write *something*.
		copy(newSeed, seedRNGFailure)
		for i := len(seedRNGFailure); i < len(newSeed); i++ {
			newSeed[i] = 0
		}
		newSeedLen = hashLen
		newSeed = newSeed[:newSeedLen]
		errs = append(errs, fmt.Errorf("read new seed: %w", err))
	}
	res.NewSeedCreditable = creditable
	res.NewSeedBytes = int(newSeedLen)

	// Fold the accumulated state into the last hashLen bytes of the new
	// seed: len prefix (as in the C impl, using platform size_t width),
	// then the raw seed bytes.
	seedLenBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(seedLenBuf, uint64(newSeedLen))
	hash.Write(seedLenBuf)
	hash.Write(newSeed)
	digest := hash.Sum(nil)
	copy(newSeed[int(newSeedLen)-hashLen:], digest[:hashLen])

	opts.Log("Saving %d bits of %s seed for next boot", newSeedLen*8, creditableWord(creditable))

	if err := writeNewSeed(opts.SeedDir, dfd, newSeed, creditable); err != nil {
		errs = append(errs, fmt.Errorf("write new seed: %w", err))
	}

	if len(errs) > 0 {
		return res, errors.Join(errs...)
	}
	return res, nil
}

func timespecNow(clockid int32) unix.Timespec {
	var ts unix.Timespec
	_ = unix.ClockGettime(clockid, &ts)
	return ts
}

func writeTimespec(w interface{ Write([]byte) (int, error) }, ts unix.Timespec) {
	// Match the C impl: dump the raw struct as-is. Field widths depend
	// on the platform, but the value is used only as internal mixing
	// input — no cross-platform interop is expected on seed content.
	buf := make([]byte, unsafe.Sizeof(ts))
	*(*unix.Timespec)(unsafe.Pointer(&buf[0])) = ts
	_, _ = w.Write(buf)
}

func creditableWord(b bool) string {
	if b {
		return "creditable"
	}
	return "non-creditable"
}

// consumeSeed reads and removes name from the seed directory, then
// feeds it to the kernel RNG. Returns (true, nil) if the file was
// present and successfully fed. A missing file is (false, nil).
func consumeSeed(dir string, dfd int, name string, credit bool, hash interface {
	Write([]byte) (int, error)
}, log func(string, ...any)) (bool, error) {
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", name, err)
	}
	// Unlink + fsync of the dir before we credit anything, so a crash
	// cannot replay this seed.
	if err := unix.Unlinkat(dfd, name, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("unlink %s: %w", name, err)
	}
	if err := unix.Fsync(dfd); err != nil {
		return false, fmt.Errorf("fsync dir: %w", err)
	}
	if len(data) == 0 {
		return false, nil
	}
	lenBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lenBuf, uint64(len(data)))
	_, _ = hash.Write(lenBuf)
	_, _ = hash.Write(data)

	adjective := "and"
	if !credit {
		adjective = "without"
	}
	log("Seeding %d bits %s crediting", len(data)*8, adjective)
	if err := feedKernelHook(data, credit); err != nil {
		return true, fmt.Errorf("RNDADDENTROPY: %w", err)
	}
	return true, nil
}

// feedKernelHook and readNewSeedHook are the seams for tests. Both
// default to the real syscalls; test code swaps them via
// pkg/rng.SetHooksForTest (implemented in the _test.go file).
var (
	feedKernelHook  = feedKernel
	readNewSeedHook = readNewSeed
)

// feedKernel writes seed bytes to /dev/urandom via the RNDADDENTROPY
// ioctl. entropyCount == len(seed)*8 requests crediting; 0 does not.
func feedKernel(seed []byte, credit bool) error {
	if len(seed) > maxSeedLen {
		return fmt.Errorf("seed too big: %d > %d", len(seed), maxSeedLen)
	}
	fd, err := unix.Open("/dev/urandom", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/urandom: %w", err)
	}
	defer unix.Close(fd)

	var req randPoolInfo
	req.BufSize = int32(len(seed))
	if credit {
		req.EntropyCount = int32(len(seed) * 8)
	}
	copy(req.Buf[:], seed)

	if _, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		uintptr(fd),
		uintptr(unix.RNDADDENTROPY),
		uintptr(unsafe.Pointer(&req)),
	); errno != 0 {
		return errno
	}
	return nil
}

// readNewSeed fills buf with random bytes. The bool return indicates
// whether the bytes came from a properly-seeded pool (GRND_NONBLOCK
// success) — that fact controls whether the *next* boot may credit.
func readNewSeed(buf []byte) (bool, error) {
	n, err := unix.Getrandom(buf, unix.GRND_NONBLOCK)
	if err == nil && n == len(buf) {
		return true, nil
	}
	// EAGAIN => pool not initialised. Fall back to GRND_INSECURE so we
	// still produce *some* bytes; the seed just won't be creditable.
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.ENOSYS) {
		n, err2 := unix.Getrandom(buf, grndInsecure)
		if err2 == nil && n == len(buf) {
			return false, nil
		}
	}
	// Last resort: /dev/urandom read. Never creditable.
	fd, ferr := unix.Open("/dev/urandom", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if ferr != nil {
		if err == nil {
			return false, ferr
		}
		return false, err
	}
	defer unix.Close(fd)
	total := 0
	for total < len(buf) {
		n, rerr := unix.Read(fd, buf[total:])
		if rerr != nil {
			if errors.Is(rerr, unix.EINTR) {
				continue
			}
			return false, rerr
		}
		if n == 0 {
			return false, fmt.Errorf("/dev/urandom: short read")
		}
		total += n
	}
	return false, nil
}

// grndInsecure is GRND_INSECURE (0x0004); not exposed as a constant by
// older x/sys releases so we hard-code it.
const grndInsecure = 0x0004

// writeNewSeed writes the new seed as non-creditable first, then
// atomically renames to creditable if the caller says so. Writing
// non-creditable first means a crash between write and rename leaves
// the next boot with a safe, non-crediting seed rather than a
// creditable seed with unknown provenance.
func writeNewSeed(dir string, dfd int, seed []byte, credit bool) error {
	nonPath := filepath.Join(dir, nonCreditableSeed)
	fd, err := unix.Openat(dfd, nonCreditableSeed,
		unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_CLOEXEC, 0o400)
	if err != nil {
		return fmt.Errorf("open %s: %w", nonPath, err)
	}
	if _, err := unix.Write(fd, seed); err != nil {
		unix.Close(fd)
		return fmt.Errorf("write %s: %w", nonPath, err)
	}
	if err := unix.Fsync(fd); err != nil {
		unix.Close(fd)
		return fmt.Errorf("fsync %s: %w", nonPath, err)
	}
	unix.Close(fd)

	if credit {
		if err := unix.Renameat(dfd, nonCreditableSeed, dfd, creditableSeed); err != nil {
			return fmt.Errorf("rename to %s: %w", creditableSeed, err)
		}
	}
	return nil
}

// determineOptimalSeedLen reads the kernel pool size and returns the
// number of bytes we should write for the *next* boot's seed. Clamped
// to [minSeedLen, maxSeedLen].
func determineOptimalSeedLen(log func(string, ...any)) uint32 {
	data, err := os.ReadFile("/proc/sys/kernel/random/poolsize")
	if err != nil {
		log("Unable to determine pool size, falling back to %d bits: %v", minSeedLen*8, err)
		return minSeedLen
	}
	bits, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		log("Unable to parse pool size %q, falling back to %d bits", string(data), minSeedLen*8)
		return minSeedLen
	}
	// poolsize is in bits; round up to bytes.
	bytesLen := uint32((bits + 7) / 8)
	if bytesLen < minSeedLen {
		return minSeedLen
	}
	if bytesLen > maxSeedLen {
		return maxSeedLen
	}
	return bytesLen
}
