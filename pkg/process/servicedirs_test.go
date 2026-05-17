package process

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureServiceDirsCreates verifies a directory (with parents) is
// created at the requested mode.
func TestEnsureServiceDirsCreates(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "var", "lib", "app")
	if err := ensureServiceDirs([]ServiceDir{{Path: p, Mode: 0o750}}, 0, 0); err != nil {
		t.Fatalf("ensureServiceDirs: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", p)
	}
	if fi.Mode().Perm() != 0o750 {
		t.Errorf("mode = %o, want 0750", fi.Mode().Perm())
	}
}

// TestEnsureServiceDirsIdempotentAndFixesMode verifies an existing
// directory is left in place but its mode is corrected.
func TestEnsureServiceDirsIdempotentAndFixesMode(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "run", "app")
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureServiceDirs([]ServiceDir{{Path: p, Mode: 0o755}}, 0, 0); err != nil {
		t.Fatalf("ensureServiceDirs: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 0755 (should be corrected)", fi.Mode().Perm())
	}
}

// TestEnsureServiceDirsMultiple verifies several specs are all created.
func TestEnsureServiceDirsMultiple(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b", "c")
	if err := ensureServiceDirs([]ServiceDir{
		{Path: a, Mode: 0o755},
		{Path: b, Mode: 0o755, Volatile: true},
	}, 0, 0); err != nil {
		t.Fatalf("ensureServiceDirs: %v", err)
	}
	for _, p := range []string{a, b} {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("expected dir %s (err=%v)", p, err)
		}
	}
}
