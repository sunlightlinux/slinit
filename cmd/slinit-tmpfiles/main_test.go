package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLineBasic(t *testing.T) {
	for _, tc := range []struct {
		in    string
		kind  string
		path  string
		mode  uint32
		hasArg bool
	}{
		{"f /run/foo 0644 - - -", "f", "/run/foo", 0644, false},
		{"d /run/dir 0755 - - -", "d", "/run/dir", 0755, false},
		{"L /etc/link - - - - /target", "L", "/etc/link", 0644, true},
		{"w /proc/sys/x - - - - some-value", "w", "/proc/sys/x", 0644, true},
		{"r /run/tmp - - - -", "r", "/run/tmp", 0644, false},
	} {
		e, err := parseLine(tc.in)
		if err != nil {
			t.Errorf("parseLine(%q): %v", tc.in, err)
			continue
		}
		if e.kind != tc.kind || e.path != tc.path || e.mode != tc.mode {
			t.Errorf("parseLine(%q) = kind=%q path=%q mode=%o, want kind=%q path=%q mode=%o",
				tc.in, e.kind, e.path, e.mode, tc.kind, tc.path, tc.mode)
		}
		if tc.hasArg && e.arg == "" {
			t.Errorf("parseLine(%q): expected non-empty arg", tc.in)
		}
	}
}

func TestApplyDirAndFileEndToEnd(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "outdir")
	file := filepath.Join(dir, "outfile")

	// d: create dir
	if err := apply(entry{kind: "d", path: dir, mode: 0755, uid: os.Getuid(), gid: os.Getgid()}); err != nil {
		t.Fatalf("apply d: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	if fi.Mode().Perm() != 0755 {
		t.Errorf("dir mode: got %o, want 0755", fi.Mode().Perm())
	}

	// f: create file (once, then a second time should be no-op)
	if err := apply(entry{kind: "f", path: file, mode: 0640, uid: os.Getuid(), gid: os.Getgid()}); err != nil {
		t.Fatalf("apply f: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if err := apply(entry{kind: "f", path: file, mode: 0640, uid: os.Getuid(), gid: os.Getgid()}); err != nil {
		t.Errorf("second apply f (already-exists): %v", err)
	}

	// L: symlink to a target
	link := filepath.Join(root, "outlink")
	if err := apply(entry{kind: "L", path: link, arg: "/tmp"}); err != nil {
		t.Fatalf("apply L: %v", err)
	}
	target, err := os.Readlink(link)
	if err != nil || target != "/tmp" {
		t.Errorf("symlink target: got %q, want /tmp", target)
	}
}

func TestApplyWriteAndRemove(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "written")
	if err := apply(entry{kind: "w", path: file, arg: "hello world"}); err != nil {
		t.Fatalf("apply w: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "hello world" {
		t.Errorf("w content: got %q, want %q", data, "hello world")
	}
	if err := apply(entry{kind: "r", path: file}); err != nil {
		t.Fatalf("apply r: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("r did not remove file: %v", err)
	}
	// r on missing file is silent
	if err := apply(entry{kind: "r", path: file}); err != nil {
		t.Errorf("r on missing file should be silent: %v", err)
	}
}

func TestApplyFileWritesArgument(t *testing.T) {
	dir := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()

	// f writes its (unescaped) argument when it creates the file.
	fpath := filepath.Join(dir, "f-new")
	e, err := parseLine(`f ` + fpath + ` 0644 - - - line1\nline2\ttab\x41\101\\`)
	if err != nil {
		t.Fatalf("parseLine f: %v", err)
	}
	e.uid, e.gid = uid, gid
	if err := apply(e); err != nil {
		t.Fatalf("apply f: %v", err)
	}
	if data, _ := os.ReadFile(fpath); string(data) != "line1\nline2\ttabAA\\" {
		t.Errorf("f content: got %q, want %q", data, "line1\nline2\ttabAA\\")
	}

	// f leaves an existing file's content alone.
	e.arg = "other"
	if err := apply(e); err != nil {
		t.Fatalf("second apply f: %v", err)
	}
	if data, _ := os.ReadFile(fpath); string(data) != "line1\nline2\ttabAA\\" {
		t.Errorf("f must not rewrite an existing file: got %q", data)
	}

	// F truncates an existing file and writes the argument.
	Fpath := filepath.Join(dir, "F-existing")
	if err := os.WriteFile(Fpath, []byte("old content that is long"), 0644); err != nil {
		t.Fatal(err)
	}
	e, err = parseLine(`F ` + Fpath + ` 0644 - - - new\n`)
	if err != nil {
		t.Fatalf("parseLine F: %v", err)
	}
	e.uid, e.gid = uid, gid
	if err := apply(e); err != nil {
		t.Fatalf("apply F: %v", err)
	}
	if data, _ := os.ReadFile(Fpath); string(data) != "new\n" {
		t.Errorf("F content: got %q, want %q", data, "new\n")
	}
}

func TestCunescape(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`plain`, "plain"},
		{`a\nb`, "a\nb"},
		{`\a\b\f\r\t\v`, "\a\b\f\r\t\v"},
		{`\\ \" \' x\sy`, `\ " ' x y`},
		{`\x7e\176`, "~~"},
	} {
		got, err := cunescape(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("cunescape(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{`trailing\`, `\q`, `\x4`, `\xzz`, `\12`, `\400`} {
		if _, err := cunescape(bad); err == nil {
			t.Errorf("cunescape(%q): expected error", bad)
		}
	}
}

// TestCollectPrecedence: for the same file name /etc beats /run beats
// /usr/lib (systemd order), and files are merged and sorted by name.
func TestCollectPrecedence(t *testing.T) {
	root := t.TempDir()
	dirs := make([]string, len(defaultDirs))
	for i, d := range defaultDirs {
		dirs[i] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[i], 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name string) {
		if err := os.WriteFile(filepath.Join(root, dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("/usr/lib/tmpfiles.d", "all.conf")
	write("/run/tmpfiles.d", "all.conf")
	write("/etc/tmpfiles.d", "all.conf")
	write("/usr/lib/tmpfiles.d", "run-usr.conf")
	write("/run/tmpfiles.d", "run-usr.conf")
	write("/usr/lib/tmpfiles.d", "only-usr.conf")

	got := collect(dirs)
	want := map[string]string{
		"all.conf":      filepath.Join(root, "/etc/tmpfiles.d/all.conf"),
		"run-usr.conf":  filepath.Join(root, "/run/tmpfiles.d/run-usr.conf"),
		"only-usr.conf": filepath.Join(root, "/usr/lib/tmpfiles.d/only-usr.conf"),
	}
	if len(got) != len(want) {
		t.Fatalf("collect: got %v, want %v", got, want)
	}
	for n, p := range want {
		if got[n] != p {
			t.Errorf("collect[%q] = %q, want %q", n, got[n], p)
		}
	}
}

// "-" in the Argument column means no argument, as in every other
// column: `f /path 0644 - - - -` creates an empty file, not one holding
// a dash.
func TestApplyFileDashArgumentIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	e, err := parseLine("f " + path + " 0644 - - - -")
	if err != nil {
		t.Fatal(err)
	}
	e.uid, e.gid = os.Getuid(), os.Getgid()
	if err := apply(e); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); len(data) != 0 {
		t.Errorf("content = %q, want empty", data)
	}
}
