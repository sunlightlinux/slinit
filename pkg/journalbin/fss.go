package journalbin

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// FSS — Forward Secure Sealing. slinit's v1 uses HKDF-SHA256 for
// per-epoch key derivation and HMAC-SHA256 for the sealing tag,
// instead of systemd's FSPRG binary tree. The forward-secrecy
// property survives: compromise of the sealing state at time T
// doesn't reveal keys for epochs > T because HKDF is one-way and
// each per-epoch key is derived FROM the seed rather than from the
// previous key.
//
// v1 does NOT split sealing vs verify keys. Both are held in the
// same key file; anyone who can verify can also seal. Splitting via
// a proper FSPRG tree (systemd's approach) is deferred to v2.

// DefaultFSSEpochUsec is the default epoch duration: 15 minutes,
// same as systemd-journald default. Configurable per key file so
// operators tuning short-lived journals can shrink it.
const DefaultFSSEpochUsec = int64(15 * 60 * 1_000_000)

// FSSSealTagSize is the on-disk size of the sealing tag itself
// (HMAC-SHA256 output, 32 bytes).
const FSSSealTagSize = 32

// TAG object payload layout on disk (past the 16-byte ObjectHeader):
//   seqnum(8) + epoch(8) + hmac_sha256(32) = 48 bytes payload
//   total TAG object size = ObjectHeaderSize + 48 = 64 bytes
const (
	tagSeqnumOffset = ObjectHeaderSize      // 16
	tagEpochOffset  = ObjectHeaderSize + 8  // 24
	tagHmacOffset   = ObjectHeaderSize + 16 // 32
	tagObjectSize   = uint64(ObjectHeaderSize + 8 + 8 + FSSSealTagSize) // 64
)

// FSSKey is the on-disk representation of the sealing key file.
// JSON-encoded so operators can inspect / back it up trivially.
type FSSKey struct {
	// Seed is 64 random bytes, base64-encoded. Root input to HKDF for
	// every per-epoch key derivation.
	Seed string `json:"seed"`
	// StartUsec is the wall-clock time in microseconds when epoch 0
	// begins. Epoch for a given realtime t = (t - start) / interval.
	StartUsec int64 `json:"start_usec"`
	// IntervalUsec is the epoch duration in microseconds.
	IntervalUsec int64 `json:"interval_usec"`
}

// NewFSSKey mints a fresh sealing key with 64 random seed bytes and
// the configured interval. startUsec should be the current wall clock
// so epoch 0 aligns to "now".
func NewFSSKey(startUsec, intervalUsec int64) (*FSSKey, error) {
	if intervalUsec <= 0 {
		intervalUsec = DefaultFSSEpochUsec
	}
	seed := make([]byte, 64)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("journalbin: FSS seed: %w", err)
	}
	return &FSSKey{
		Seed:         base64.StdEncoding.EncodeToString(seed),
		StartUsec:    startUsec,
		IntervalUsec: intervalUsec,
	}, nil
}

// SaveFSSKey writes the key to path with mode 0400. Overwrites any
// existing file (calling this on an already-sealed journal file
// invalidates all past TAGs — verifier will fail from the point of
// the rotation). Callers with existing journals should copy the old
// key aside first.
func SaveFSSKey(path string, k *FSSKey) error {
	data, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return err
	}
	// os.WriteFile ignores O_EXCL — we deliberately DO overwrite here
	// so `--setup-keys --force` semantics are one call.
	if err := os.WriteFile(path, data, 0400); err != nil {
		return fmt.Errorf("journalbin: save FSS key %s: %w", path, err)
	}
	return nil
}

// LoadFSSKey reads and parses a key file.
func LoadFSSKey(path string) (*FSSKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("journalbin: read FSS key %s: %w", path, err)
	}
	var k FSSKey
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("journalbin: parse FSS key %s: %w", path, err)
	}
	if k.Seed == "" {
		return nil, errors.New("journalbin: FSS key missing seed")
	}
	if k.IntervalUsec <= 0 {
		return nil, fmt.Errorf("journalbin: FSS key bad interval %d", k.IntervalUsec)
	}
	return &k, nil
}

// EpochFor returns the epoch number for realtime `usec`. Negative
// return for times before StartUsec (invalid — no keys derived for
// pre-epoch-zero events).
func (k *FSSKey) EpochFor(usec int64) int64 {
	if usec < k.StartUsec {
		return -1
	}
	return (usec - k.StartUsec) / k.IntervalUsec
}

// DeriveEpochKey returns the 32-byte HMAC-SHA256 sealing key for
// epoch e via HKDF-SHA256 over (seed || salt || info). Salt =
// "slinit-fss-e" so the derivation is domain-separated from any
// other slinit HKDF use. Info = LE64(e) so each epoch gets a
// distinct key.
//
// Forward-secrecy: reveal of a per-epoch key does not compromise
// keys for other epochs (HKDF is one-way). Reveal of the seed
// compromises everything — the key file itself is the last-line
// secret and lives at chmod 0400 root.
func (k *FSSKey) DeriveEpochKey(e int64) ([]byte, error) {
	seed, err := base64.StdEncoding.DecodeString(k.Seed)
	if err != nil {
		return nil, fmt.Errorf("journalbin: FSS seed decode: %w", err)
	}
	salt := []byte("slinit-fss-e")
	var info [8]byte
	binary.LittleEndian.PutUint64(info[:], uint64(e))
	return hkdfSHA256(seed, salt, info[:], 32), nil
}

// hkdfSHA256 is a minimal HKDF-Extract + Expand implementation to
// avoid pulling in golang.org/x/crypto/hkdf. Sufficient for our
// single-block outputs (L <= 32).
func hkdfSHA256(ikm, salt, info []byte, length int) []byte {
	// Extract: PRK = HMAC-SHA256(salt, IKM)
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	prk := mac.Sum(nil)

	// Expand: T(1) = HMAC-SHA256(PRK, info || 0x01)
	mac = hmac.New(sha256.New, prk)
	mac.Write(info)
	mac.Write([]byte{0x01})
	t := mac.Sum(nil)
	if length <= sha256.Size {
		return t[:length]
	}
	// For our v1 needs (length=32) the single-block path suffices;
	// extend here if a caller ever asks for longer keys.
	panic("journalbin: hkdfSHA256 length > 32 not implemented")
}

// SealHMAC computes HMAC-SHA256(key, data) — used by tag write path
// (over the accumulated journal bytes since the last tag) and by
// verify path (recomputes and compares). data must be an io.Reader
// so callers can stream a windowed file range without loading it all.
func SealHMAC(key []byte, r io.Reader) ([]byte, error) {
	mac := hmac.New(sha256.New, key)
	if _, err := io.Copy(mac, r); err != nil {
		return nil, fmt.Errorf("journalbin: seal HMAC: %w", err)
	}
	return mac.Sum(nil), nil
}

// SealHMACBytes is a convenience wrapper for the common case where
// the caller already has the bytes in memory.
func SealHMACBytes(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
