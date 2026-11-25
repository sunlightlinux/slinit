package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// stMode returns the permission and special bits of path, as stat(2)
// reports them (os.FileMode keeps the special bits elsewhere).
func stMode(t *testing.T, path string) uint32 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Mode & 0o7777
}

// The sticky, setgid and setuid bits of a Mode column must reach the
// inode. The mode was handed to Go as os.FileMode(e.mode), but Go keeps
// those bits outside the low 12 (os.ModeSticky and friends), so 01000
// was simply dropped: `d /var/tmp 1777` made a world-writable directory
// without the sticky bit, and on an existing /var/tmp the chmod stripped
// it at every boot.
func TestDirKeepsSpecialModeBits(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		mode uint32
		pre  bool // the directory exists before the entry runs
	}{
		{"new-sticky", 0o1777, false},
		{"existing-sticky", 0o1777, true},
		{"new-setgid", 0o2755, false},
	} {
		p := filepath.Join(root, tc.name)
		if tc.pre {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Chmod(p, tc.mode); err != nil {
				t.Fatal(err)
			}
		}
		e := entry{kind: "d", path: p, mode: tc.mode, uid: -1, gid: -1}
		if err := apply(e); err != nil {
			t.Fatalf("%s: apply: %v", tc.name, err)
		}
		if got := stMode(t, p); got != tc.mode {
			t.Errorf("%s: d %#o left the directory at %#o", tc.name, tc.mode, got)
		}
	}
}

// z sets the mode of an existing path; the special bits go with it.
func TestChmodEntryKeepsSpecialModeBits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "spool")
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
	e := entry{kind: "z", path: p, mode: 0o3775, uid: -1, gid: -1}
	if err := apply(e); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := stMode(t, p); got != 0o3775 {
		t.Errorf("z 03775 left the directory at %#o", got)
	}
}

// The owner is set before the mode: chown(2) on a regular file clears
// setuid and setgid — even with -1/-1 — so chmod first and chown second
// lost them again on z/Z.
func TestChmodEntryKeepsSetuidOnAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(p, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	e := entry{kind: "z", path: p, mode: 0o4755, uid: -1, gid: -1}
	if err := apply(e); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := stMode(t, p); got != 0o4755 {
		t.Errorf("z 04755 left the file at %#o", got)
	}
}

// f and F leave the file at the entry's mode. The mode was only the
// open(2) create mode, so the umask cut it on a new file, and F on an
// existing file kept whatever mode it had — `F /run/secret 0600` left a
// 0644 file readable by all, with the new content in it.
func TestFileEntriesSetTheMode(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	root := t.TempDir()

	existing := filepath.Join(root, "secret")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range []entry{
		{kind: "F", path: existing, mode: 0o600, uid: -1, gid: -1, arg: "token"},
		{kind: "f", path: filepath.Join(root, "shared"), mode: 0o666, uid: -1, gid: -1},
		{kind: "F", path: filepath.Join(root, "fresh"), mode: 0o664, uid: -1, gid: -1},
		{kind: "f", path: filepath.Join(root, "helper"), mode: 0o4755, uid: -1, gid: -1},
	} {
		if err := apply(e); err != nil {
			t.Fatalf("%s %s: apply: %v", e.kind, e.path, err)
		}
		if got := stMode(t, e.path); got != e.mode {
			t.Errorf("%s %s %#o left the file at %#o", e.kind, filepath.Base(e.path), e.mode, got)
		}
	}
}
