package checkpath

import (
	"os"
	"path/filepath"
	"testing"
)

// swapAfterOpen makes Apply's target a symlink to victim right after
// Apply has opened it, which is the window the old path-based code lost:
// it checked the entry with Lstat, then chmod/chown/truncated it by name.
// The entry Apply opened is moved to path+".orig".
func swapAfterOpen(t *testing.T, path, victim string) {
	t.Helper()
	afterOpenHook = func() {
		if err := os.Rename(path, path+".orig"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterOpenHook = nil })
}

func TestApplyChmodSurvivesSwap(t *testing.T) {
	dir := t.TempDir()
	path, victim := filepath.Join(dir, "f"), filepath.Join(dir, "victim")
	for _, p := range []string{path, victim} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	swapAfterOpen(t, path, victim)

	if _, err := Apply(Spec{Path: path, Type: TypeFile, Mode: 0o600, Owner: Owner{-1, -1}}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(victim); st.Mode().Perm() != 0o644 {
		t.Errorf("victim mode = %v: chmod followed the swapped-in symlink", st.Mode().Perm())
	}
	if st, _ := os.Stat(path + ".orig"); st.Mode().Perm() != 0o600 {
		t.Errorf("checked inode mode = %v, want 0600: the change missed it", st.Mode().Perm())
	}
}

func TestApplyFileTruncateSurvivesSwap(t *testing.T) {
	dir := t.TempDir()
	path, victim := filepath.Join(dir, "f"), filepath.Join(dir, "victim")
	for _, p := range []string{path, victim} {
		if err := os.WriteFile(p, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	swapAfterOpen(t, path, victim)

	if _, err := Apply(Spec{Path: path, Type: TypeFile, Truncate: true, Owner: Owner{-1, -1}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep me" {
		t.Errorf("victim = %q: truncate followed the swapped-in symlink", b)
	}
	if b, _ := os.ReadFile(path + ".orig"); len(b) != 0 {
		t.Errorf("checked file still has %q: the truncate missed it", b)
	}
}

// -D empties a directory. By name, the swap made it empty the victim.
func TestApplyDirTruncateSurvivesSwap(t *testing.T) {
	dir := t.TempDir()
	path, victim := filepath.Join(dir, "d"), filepath.Join(dir, "victimdir")
	for _, d := range []string{path, victim} {
		if err := os.MkdirAll(filepath.Join(d, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "sub", "file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	swapAfterOpen(t, path, victim)

	if _, err := Apply(Spec{Path: path, Type: TypeDir, Truncate: true, Owner: Owner{-1, -1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(victim, "sub", "file")); err != nil {
		t.Errorf("victim directory was emptied through the swapped-in symlink: %v", err)
	}
	if entries, _ := os.ReadDir(path + ".orig"); len(entries) != 0 {
		t.Errorf("checked directory still holds %d entries: -D missed it", len(entries))
	}
}
