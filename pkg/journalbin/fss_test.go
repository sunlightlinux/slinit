package journalbin

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestNewFSSKeyMintsFreshSeed(t *testing.T) {
	k1, err := NewFSSKey(1_000_000, DefaultFSSEpochUsec)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := NewFSSKey(1_000_000, DefaultFSSEpochUsec)
	if err != nil {
		t.Fatal(err)
	}
	if k1.Seed == k2.Seed {
		t.Fatal("two fresh keys share a seed — RNG broken")
	}
	if k1.StartUsec != 1_000_000 || k1.IntervalUsec != DefaultFSSEpochUsec {
		t.Errorf("start/interval not persisted: %+v", k1)
	}
}

func TestSaveLoadFSSKeyRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal-key")
	orig, _ := NewFSSKey(2_000_000, 60_000_000)
	if err := SaveFSSKey(path, orig); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFSSKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Seed != orig.Seed || loaded.StartUsec != orig.StartUsec || loaded.IntervalUsec != orig.IntervalUsec {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", loaded, orig)
	}
}

func TestLoadFSSKeyRejectsBad(t *testing.T) {
	tmp := t.TempDir()
	// Missing file.
	if _, err := LoadFSSKey(filepath.Join(tmp, "nope")); err == nil {
		t.Fatal("expected error for missing file")
	}
	// Bad JSON.
	bad := filepath.Join(tmp, "bad")
	if err := writeAll(bad, []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFSSKey(bad); err == nil {
		t.Fatal("expected error for bad JSON")
	}
	// Empty seed.
	empty := filepath.Join(tmp, "empty")
	if err := writeAll(empty, []byte(`{"seed":"","start_usec":0,"interval_usec":60}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFSSKey(empty); err == nil {
		t.Fatal("expected error for empty seed")
	}
	// Zero interval.
	badInt := filepath.Join(tmp, "badint")
	if err := writeAll(badInt, []byte(`{"seed":"aGVsbG8=","start_usec":0,"interval_usec":0}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFSSKey(badInt); err == nil {
		t.Fatal("expected error for zero interval")
	}
}

func TestEpochFor(t *testing.T) {
	k := &FSSKey{StartUsec: 1000, IntervalUsec: 100}
	cases := []struct {
		usec int64
		want int64
	}{
		{500, -1},   // before start
		{1000, 0},   // exact start → epoch 0
		{1099, 0},   // in epoch 0
		{1100, 1},   // rollover
		{1250, 2},   // mid-epoch 2
		{9000, 80},  // far future
	}
	for _, c := range cases {
		if got := k.EpochFor(c.usec); got != c.want {
			t.Errorf("EpochFor(%d) = %d, want %d", c.usec, got, c.want)
		}
	}
}

func TestDeriveEpochKeyDistinct(t *testing.T) {
	k, _ := NewFSSKey(0, 60_000_000)
	k0, err := k.DeriveEpochKey(0)
	if err != nil {
		t.Fatal(err)
	}
	k1, err := k.DeriveEpochKey(1)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(k0, k1) {
		t.Fatal("keys for different epochs must differ")
	}
	if len(k0) != 32 {
		t.Fatalf("epoch key length = %d, want 32", len(k0))
	}
	// Same epoch → deterministic output.
	k0b, _ := k.DeriveEpochKey(0)
	if !bytes.Equal(k0, k0b) {
		t.Fatal("epoch key derivation not deterministic")
	}
}

func TestSealHMACRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	data := []byte("some journal bytes to seal")
	a := SealHMACBytes(key, data)
	b, err := SealHMAC(key, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("stream and bytes HMAC disagree")
	}
	if len(a) != FSSSealTagSize {
		t.Fatalf("HMAC size = %d, want %d", len(a), FSSSealTagSize)
	}
	// Different key → different tag.
	key2 := make([]byte, 32)
	if bytes.Equal(SealHMACBytes(key2, data), a) {
		t.Fatal("HMAC ignored key change")
	}
}

func writeAll(path string, data []byte) error {
	// Small helper so tests don't rely on os.WriteFile mode side-effects.
	return writeFileWithMode(path, data, 0644)
}
