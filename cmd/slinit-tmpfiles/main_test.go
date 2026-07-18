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
