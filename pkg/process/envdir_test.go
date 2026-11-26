package process

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReadEnvDir is runit's envdir(8) as slinit-service(5) documents it:
// one variable per file, the file name is the name, the first line is
// the value, an empty file removes the variable.
func TestReadEnvDirValues(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"PLAIN":     "value\n",
		"NOEOL":     "value",
		"MULTILINE": "first\nsecond\n",
		"NUL":       "a\x00b\x00\n", // runit: NUL in the value is a newline
		"TRAILWS":   "trail  \t \n",
		"LEADWS":    "  lead",
		"CRLF":      "dos\r\n", // only spaces and tabs are trimmed
		"INNER":     "a b\tc",
		"EQUALS":    "k=v=w",
		"BINARY":    "\xff\xfe",
		"EMPTY":     "",
		"BLANKLINE": "\nignored",
		"SPACES":    "   \n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env, err := ReadEnvDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PLAIN":     "value",
		"NOEOL":     "value",
		"MULTILINE": "first",
		"NUL":       "a\nb\n",
		"TRAILWS":   "trail",
		"LEADWS":    "  lead",
		"CRLF":      "dos\r",
		"INNER":     "a b\tc",
		"EQUALS":    "k=v=w",
		"BINARY":    "\xff\xfe",
		// An empty file is present with "" (the caller unsets it).
		"EMPTY": "",
		// So is a first line that is empty, or blank once trimmed: the
		// map cannot tell these from an empty file, so they unset too
		// (runit's envdir would set them to the empty string).
		"BLANKLINE": "",
		"SPACES":    "",
	}
	for k, v := range want {
		got, ok := env[k]
		if !ok {
			t.Errorf("%s missing", k)
		} else if got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("got %d variables, want %d: %q", len(env), len(want), env)
	}
}

// Hidden files, names with '=', directories and anything unreadable are
// skipped rather than failing the whole directory.
func TestReadEnvDirSkips(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("KEEP", "yes")
	write(".hidden", "no")
	write("A=B", "no")
	if err := os.Mkdir(filepath.Join(dir, "SUBDIR"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("SUBDIR/INNER", "no") // not recursed into
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("linked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "LINK")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "DIRLINK")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "DANGLING")); err != nil {
		t.Fatal(err)
	}
	write("UNREADABLE", "secret")
	if err := os.Chmod(filepath.Join(dir, "UNREADABLE"), 0); err != nil {
		t.Fatal(err)
	}

	env, err := ReadEnvDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"KEEP": "yes", "LINK": "linked"}
	if os.Geteuid() == 0 {
		want["UNREADABLE"] = "secret" // root reads mode 0 files
	}
	if len(env) != len(want) {
		t.Errorf("got %q, want %q", env, want)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestReadEnvDirErrors(t *testing.T) {
	if _, err := ReadEnvDir(filepath.Join(t.TempDir(), "missing")); err == nil ||
		!strings.HasPrefix(err.Error(), "read env-dir: ") || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing dir: err = %v, want a wrapped not-exist error", err)
	}
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnvDir(f); err == nil {
		t.Error("a regular file read as an env-dir")
	}
	env, err := ReadEnvDir(t.TempDir())
	if err != nil || env == nil || len(env) != 0 {
		t.Errorf("empty dir: %q, %v; want an empty non-nil map", env, err)
	}
}

// An env-dir entry that resolves to a device used to be read to its
// end: /dev/zero has none, so PID 1 allocated until it died. Only
// regular files are read; the symlink to one still is (runit follows
// symlinks in an envdir).
func TestReadEnvDirSkipsDevices(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "ZERO")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "LINKED")); err != nil {
		t.Fatal(err)
	}
	env, err := ReadEnvDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["ZERO"]; ok {
		t.Error("ZERO (a device) was read")
	}
	if env["LINKED"] != "v" {
		t.Errorf("LINKED = %q, want v", env["LINKED"])
	}
}
