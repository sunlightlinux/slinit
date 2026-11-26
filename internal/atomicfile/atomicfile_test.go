package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// noTemps fails if a temp file was left in dir.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if len(m) != 0 {
		t.Errorf("temp files left behind: %v", m)
	}
}

// writeCases runs fn with renameat2 and with the link(2) fallback a
// filesystem without RENAME_NOREPLACE takes.
func writeCases(t *testing.T, fn func(t *testing.T)) {
	t.Run("renameat2", fn)
	t.Run("link-fallback", func(t *testing.T) {
		saved := renameNoReplace
		renameNoReplace = func(string, string) error { return unix.EINVAL }
		t.Cleanup(func() { renameNoReplace = saved })
		fn(t)
	})
}

func TestWriteNewFile(t *testing.T) {
	writeCases(t, func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "svc")
		if err := Write(dst, []byte("type = process\n"), 0o644, false); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, dst); got != "type = process\n" {
			t.Errorf("content = %q", got)
		}
		fi, _ := os.Lstat(dst)
		if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o644 {
			t.Errorf("mode = %v, want regular 0644", fi.Mode())
		}
		noTemps(t, dir)
	})
}

// A symlink at the destination must never be written through: it
// pointed the converters' os.WriteFile at a file outside the output
// directory. It is refused with and without overwrite, and its target
// is untouched.
func TestWriteRefusesSymlink(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		writeCases(t, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "out")
			os.Mkdir(out, 0o755)
			victim := filepath.Join(dir, "victim")
			os.WriteFile(victim, []byte("precious\n"), 0o600)
			dst := filepath.Join(out, "svc")
			os.Symlink("../victim", dst)

			err := Write(dst, []byte("evil\n"), 0o644, overwrite)
			if !errors.Is(err, ErrSymlink) {
				t.Errorf("overwrite=%v: err = %v, want ErrSymlink", overwrite, err)
			}
			if got := readFile(t, victim); got != "precious\n" {
				t.Errorf("victim rewritten: %q", got)
			}
			if fi, _ := os.Lstat(dst); fi.Mode()&fs.ModeSymlink == 0 {
				t.Errorf("symlink replaced")
			}
			noTemps(t, out)
		})
	}
}

// A dangling symlink is refused too (os.Stat said "not there").
func TestWriteRefusesDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "svc")
	os.Symlink(filepath.Join(dir, "nowhere"), dst)
	if err := Write(dst, []byte("x"), 0o644, false); !errors.Is(err, ErrSymlink) {
		t.Errorf("err = %v, want ErrSymlink", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere")); err == nil {
		t.Errorf("symlink target created")
	}
}

func TestWriteExisting(t *testing.T) {
	writeCases(t, func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "svc")
		os.WriteFile(dst, []byte("hand-edited\n"), 0o644)

		err := Write(dst, []byte("generated\n"), 0o644, false)
		if !errors.Is(err, ErrExists) || !errors.Is(err, fs.ErrExist) {
			t.Errorf("err = %v, want ErrExists", err)
		}
		if got := readFile(t, dst); got != "hand-edited\n" {
			t.Errorf("existing file clobbered without overwrite: %q", got)
		}
		if err := Write(dst, []byte("generated\n"), 0o755, true); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, dst); got != "generated\n" {
			t.Errorf("overwrite: content = %q", got)
		}
		if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o755 {
			t.Errorf("overwrite: mode = %v, want 0755", fi.Mode())
		}
		noTemps(t, dir)
	})
}

// The no-clobber check happens again at the rename: a file that
// appears between the check and the rename is not replaced.
func TestInstallNoReplaceRace(t *testing.T) {
	writeCases(t, func(t *testing.T) {
		dir := t.TempDir()
		dst := filepath.Join(dir, "svc")
		tmp := filepath.Join(dir, ".svc.tmp-1")
		os.WriteFile(tmp, []byte("generated\n"), 0o644)
		os.WriteFile(dst, []byte("raced in\n"), 0o644)
		if err := install(tmp, dst, false); !errors.Is(err, ErrExists) {
			t.Errorf("err = %v, want ErrExists", err)
		}
		if got := readFile(t, dst); got != "raced in\n" {
			t.Errorf("raced file replaced: %q", got)
		}
	})
}

func TestWriteRefusesDirectory(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "svc")
	os.Mkdir(dst, 0o755)
	if err := Write(dst, []byte("x"), 0o644, true); err == nil {
		t.Errorf("wrote over a directory")
	}
	noTemps(t, dir)
}

// A temp-like name planted next to the destination is never used:
// the temp file is created O_EXCL under a random name.
func TestWriteIgnoresPlantedTemp(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("precious\n"), 0o600)
	dst := filepath.Join(dir, "svc")
	os.Symlink(victim, dst+".tmp")
	if err := Write(dst, []byte("generated\n"), 0o644, false); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, victim); got != "precious\n" {
		t.Errorf("victim rewritten: %q", got)
	}
	if got := readFile(t, dst); got != "generated\n" {
		t.Errorf("content = %q", got)
	}
}

// A new file gets mode less the umask, as os.WriteFile gives it: the
// temp file used to be chmodded to the full mode, so umask 077 still
// produced a world-readable 0644.
func TestWriteNewFileHonoursUmask(t *testing.T) {
	writeCases(t, func(t *testing.T) {
		saved := unix.Umask(0o077)
		defer unix.Umask(saved)
		dir := t.TempDir()
		dst := filepath.Join(dir, "svc")
		if err := Write(dst, []byte("x\n"), 0o644, false); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v under umask 077, want 0600", fi.Mode())
		}
	})
}

// Replacing a file keeps its permission bits, as os.WriteFile (which
// truncates in place) keeps them: a 0600 file is not opened up to the
// requested 0644. Execute bits asked for are added only where that
// class may already read.
func TestWriteOverwriteKeepsMode(t *testing.T) {
	for _, c := range []struct {
		old, req, want fs.FileMode
	}{
		{0o600, 0o644, 0o600},
		{0o640, 0o644, 0o640},
		{0o600, 0o755, 0o700},
		{0o644, 0o755, 0o755},
		{0o755, 0o644, 0o755},
	} {
		dir := t.TempDir()
		dst := filepath.Join(dir, "svc")
		os.WriteFile(dst, []byte("old\n"), 0o600)
		os.Chmod(dst, c.old)
		if err := Write(dst, []byte("new\n"), c.req, true); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(dst); fi.Mode().Perm() != c.want {
			t.Errorf("old %v, requested %v: mode = %v, want %v", c.old, c.req, fi.Mode().Perm(), c.want)
		}
		noTemps(t, dir)
	}
}

// As root, replacing a file keeps its owner and group; rename would
// otherwise hand a user's file to root.
func TestWriteOverwriteKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to own a file as someone else")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "svc")
	os.WriteFile(dst, []byte("old\n"), 0o644)
	if err := os.Chown(dst, 65534, 65534); err != nil {
		t.Skip(err)
	}
	if err := Write(dst, []byte("new\n"), 0o644, true); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dst)
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 65534 || st.Gid != 65534 {
		t.Errorf("owner = %d:%d, want 65534:65534", st.Uid, st.Gid)
	}
}
