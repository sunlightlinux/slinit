package safepath

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root every link and directory here is root's, which the rule trusts")
	}
}

func mustOpen(t *testing.T, dir string) {
	t.Helper()
	fd, err := OpenDir(dir)
	if err != nil {
		t.Fatalf("OpenDir(%s): %v", dir, err)
	}
	unix.Close(fd)
}

func mustRefuse(t *testing.T, dir string) {
	t.Helper()
	fd, err := OpenDir(dir)
	if err == nil {
		unix.Close(fd)
		t.Fatalf("OpenDir(%s): followed an unsafe symlink", dir)
	}
}

// Root's links in root's directories are followed: /proc/self -> <pid>,
// and /var/run -> /run where the system has it.
func TestFollowsRootSymlinks(t *testing.T) {
	mustOpen(t, "/proc/self/fd")
	if fi, err := os.Lstat("/var/run"); err == nil && fi.Mode()&os.ModeSymlink != 0 &&
		fi.Sys().(*syscall.Stat_t).Uid == 0 {
		mustOpen(t, "/var/run")
	}
}

// A user's link to a place that user owns, by a relative target, is
// followed: the lookup never leaves that user's files.
func TestFollowsSameOwnerRelativeSymlink(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(base, "rel")); err != nil {
		t.Fatal(err)
	}
	mustOpen(t, filepath.Join(base, "rel"))
}

// A user's link with an absolute target steps from that user's link to
// root's "/": an unsafe transition, whatever it points at.
func TestRefusesUserSymlinkToAbsolutePath(t *testing.T) {
	skipIfRoot(t)
	base := t.TempDir()
	if err := os.Symlink(base, filepath.Join(base, "abs")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, filepath.Join(base, "abs"))
}

// Climbing out of a user's directory into root's through ".." is the
// same transition.
func TestRefusesDotDotIntoRootDirectory(t *testing.T) {
	skipIfRoot(t)
	base := t.TempDir()
	var st unix.Stat_t
	up := base
	for {
		up = filepath.Dir(up)
		if err := unix.Stat(up, &st); err != nil {
			t.Fatal(err)
		}
		if st.Uid == 0 {
			break
		}
	}
	rel, err := filepath.Rel(base, up)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rel, filepath.Join(base, "up")); err != nil {
		t.Fatal(err)
	}
	mustRefuse(t, filepath.Join(base, "up"))
}

// A user's link in root's directory (the sticky /tmp) is not followed,
// even with a relative target into the user's own files: the link must
// be root's or the directory owner's.
func TestRefusesUserSymlinkInRootDirectory(t *testing.T) {
	skipIfRoot(t)
	tmp := "/tmp"
	var st unix.Stat_t
	if err := unix.Stat(tmp, &st); err != nil || st.Uid != 0 {
		t.Skip("/tmp is not root's")
	}
	base, err := os.MkdirTemp(tmp, "safepath")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	link := base + ".link"
	if err := os.Symlink(filepath.Base(base), link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(link) })
	mustRefuse(t, link)
}

func TestMkdirParentCreatesMissing(t *testing.T) {
	base := t.TempDir()
	fd, name, err := MkdirParent(filepath.Join(base, "a", "b", "leaf"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if name != "leaf" {
		t.Errorf("name = %q, want leaf", name)
	}
	if fi, err := os.Stat(filepath.Join(base, "a", "b")); err != nil || !fi.IsDir() {
		t.Errorf("a/b not created: %v", err)
	}
}

func TestOpenParentRejectsNoName(t *testing.T) {
	for _, p := range []string{"/", ".", "a/.."} {
		if fd, _, err := OpenParent(p); err == nil {
			unix.Close(fd)
			t.Errorf("OpenParent(%q): no error", p)
		}
	}
}
