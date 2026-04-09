package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFileAt writes name with the given mtime, returning the full path.
func writeFileAt(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

func TestEvalFileCompareNewer(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	older := writeFileAt(t, dir, "old", now.Add(-1*time.Hour))
	newer := writeFileAt(t, dir, "new", now)

	if code, _ := evalFileCompare("is-newer-than", newer, older); code != 0 {
		t.Errorf("newer > older: expected 0, got %d", code)
	}
	if code, _ := evalFileCompare("is-newer-than", older, newer); code != 1 {
		t.Errorf("older > newer: expected 1, got %d", code)
	}
}

func TestEvalFileCompareOlder(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	older := writeFileAt(t, dir, "old", now.Add(-1*time.Hour))
	newer := writeFileAt(t, dir, "new", now)

	if code, _ := evalFileCompare("is-older-than", older, newer); code != 0 {
		t.Errorf("older < newer: expected 0, got %d", code)
	}
	if code, _ := evalFileCompare("is-older-than", newer, older); code != 1 {
		t.Errorf("newer < older: expected 1, got %d", code)
	}
}

func TestEvalFileCompareSameMtime(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().Add(-1 * time.Minute)
	a := writeFileAt(t, dir, "a", ts)
	b := writeFileAt(t, dir, "b", ts)

	// Neither after nor before → both operations return 1.
	if code, _ := evalFileCompare("is-newer-than", a, b); code != 1 {
		t.Errorf("same mtime is-newer-than: expected 1, got %d", code)
	}
	if code, _ := evalFileCompare("is-older-than", a, b); code != 1 {
		t.Errorf("same mtime is-older-than: expected 1, got %d", code)
	}
}

func TestEvalFileCompareMissingFile(t *testing.T) {
	dir := t.TempDir()
	existing := writeFileAt(t, dir, "existing", time.Now())
	missing := filepath.Join(dir, "missing")

	// Missing operand → exit 1 (false), not a hard error.
	if code, err := evalFileCompare("is-newer-than", missing, existing); code != 1 || err != nil {
		t.Errorf("missing a: expected (1,nil), got (%d,%v)", code, err)
	}
	if code, err := evalFileCompare("is-newer-than", existing, missing); code != 1 || err != nil {
		t.Errorf("missing b: expected (1,nil), got (%d,%v)", code, err)
	}
	if code, err := evalFileCompare("is-older-than", missing, existing); code != 1 || err != nil {
		t.Errorf("missing a older: expected (1,nil), got (%d,%v)", code, err)
	}
}

func TestEvalFileCompareBothMissing(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "nope-a")
	b := filepath.Join(dir, "nope-b")
	if code, err := evalFileCompare("is-newer-than", a, b); code != 1 || err != nil {
		t.Errorf("both missing: expected (1,nil), got (%d,%v)", code, err)
	}
}
