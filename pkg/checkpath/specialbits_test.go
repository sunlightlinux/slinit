package checkpath

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func stMode(t *testing.T, path string) uint32 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Mode & 0o7777
}

// -m 1777 must give a sticky directory. ParseMode returns the raw octal
// number, but the mode was masked with os.ModePerm (0777) and passed to
// os.Chmod, which only knows the special bits as os.ModeSticky and
// friends — so 01000, 02000 and 04000 never reached the inode, and an
// existing 1777 directory looked "already correct" at 0777.
func TestApplyKeepsSpecialModeBits(t *testing.T) {
	base := t.TempDir()
	for _, tc := range []struct {
		name string
		typ  PathType
		mode string
		pre  func(string) error
	}{
		{"new-sticky-dir", TypeDir, "1777", nil},
		{"existing-sticky-dir", TypeDir, "1777", func(p string) error {
			if err := os.Mkdir(p, 0o755); err != nil {
				return err
			}
			return unix.Chmod(p, 0o777) // past the umask
		}},
		{"new-setgid-dir", TypeDir, "2775", nil},
		{"existing-setuid-file", TypeFile, "4755", func(p string) error { return os.WriteFile(p, nil, 0o755) }},
	} {
		p := filepath.Join(base, tc.name)
		if tc.pre != nil {
			if err := tc.pre(p); err != nil {
				t.Fatal(err)
			}
		}
		m, err := ParseMode(tc.mode)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(Spec{Path: p, Type: tc.typ, Mode: m, Owner: Owner{UID: -1, GID: -1}}); err != nil {
			t.Fatalf("%s: apply: %v", tc.name, err)
		}
		if got := stMode(t, p); got != uint32(m) {
			t.Errorf("%s: -m %s left the path at %#o", tc.name, tc.mode, got)
		}
	}
}

// The owner is set before the mode: chown(2) on a regular file clears
// setuid/setgid, so chmod first and chown second lost them. Changing
// the owner to another id needs root. Real root: a user namespace will
// not do, since every host directory there is owned by the overflow uid
// and safepath (rightly) refuses the step from it into the test's own
// directory.
func TestApplyKeepsSetuidAcrossChown(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs real root to change a file's owner (a user namespace cannot pass safepath's owner checks)")
	}
	p := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(Spec{Path: p, Type: TypeFile, Mode: 0o4755, Owner: Owner{UID: 1, GID: 1}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := stMode(t, p); got != 0o4755 {
		t.Errorf("-m 4755 -o 1:1 left the file at %#o", got)
	}
}
