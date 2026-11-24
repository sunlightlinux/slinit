package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A recursive Z entry must not reach outside the tree it names.
//
// applyChown's recursive walk calls os.Chmod and os.Chown on each path
// it visits. filepath.Walk uses Lstat, so it does not descend into a
// symlink — but it does visit it, and os.Chmod/os.Chown follow one. A
// symlink planted inside a tree that slinit-tmpfiles later fixes up as
// root therefore has its *target* re-moded and re-owned, anywhere on
// the filesystem.
//
// This is the shape finit moved away from in 9bfbdc6b/88a9e657 by doing
// the work through an fd (fstat/fchmod/fchown), and the reason systemd's
// tmpfiles opens with O_NOFOLLOW.
func TestRecursiveChownDoesNotFollowSymlinksOutOfTree(t *testing.T) {
	root := t.TempDir()

	// The file that must not be touched. 0600 and outside the tree.
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The tree the entry names, with a symlink pointing out of it.
	tree := filepath.Join(root, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "link")); err != nil {
		t.Fatal(err)
	}

	before, err := os.Lstat(outside)
	if err != nil {
		t.Fatal(err)
	}

	// Z /tree 0755 — recursive mode/owner fixup, as root would run it.
	// The mode has to keep the directory traversable: 0666 on the tree
	// root strips +x and the walk then cannot read its own tree, which
	// is a separate wart and not what this probes.
	// uid/gid are left as -1 so the test needs no privilege: os.Chown
	// with -1 changes neither, which keeps the mode half as the probe.
	e := entry{kind: "Z", path: tree, mode: 0o755, uid: -1, gid: -1}
	if err := applyChown(e, true); err != nil {
		t.Fatalf("applyChown: %v", err)
	}

	after, err := os.Lstat(outside)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("a recursive Z on %s changed the mode of %s through a symlink: %v → %v",
			tree, outside, before.Mode().Perm(), after.Mode().Perm())
	}
}

// The same rule for a plain z naming a symlink directly. Before, the
// Lstat was taken but only to decide whether to recurse; the chmod that
// followed it used the path and so followed the link.
func TestNonRecursiveChownOnASymlinkDoesNotTouchItsTarget(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	e := entry{kind: "z", path: link, mode: 0o644, uid: -1, gid: -1}
	if err := applyChown(e, false); err != nil {
		t.Fatalf("applyChown: %v", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("z on the symlink %s changed its target's mode to %v; want 0600 untouched",
			link, fi.Mode().Perm())
	}
}

// An F entry must not write through a symlink. F opens with
// O_CREATE|O_WRONLY|O_TRUNC and no O_EXCL, so without O_NOFOLLOW it
// opens whatever the link names and truncates it — a plain f is saved
// only by O_EXCL, which refuses a symlink and falls through to the
// chown path instead.
func TestForcedFileDoesNotWriteThroughASymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	e := entry{kind: "F", path: link, mode: 0o644, uid: -1, gid: -1, arg: "clobbered"}
	_ = applyFile(e, true)

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "must survive" {
		t.Errorf("F on the symlink %s rewrote its target: %q", link, string(got))
	}
}

// Same class for w, which is the one type whose whole job is to write
// into a file that already exists. os.WriteFile is
// O_WRONLY|O_CREATE|O_TRUNC and follows a symlink.
func TestWriteDoesNotWriteThroughASymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	e := entry{kind: "w", path: link, arg: "clobbered"}
	_ = applyWrite(e)

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "must survive" {
		t.Errorf("w on the symlink %s rewrote its target: %q", link, string(got))
	}
}

// A d or D entry whose path is a symlink must not change the directory
// it points at. os.MkdirAll succeeds on a symlink to a directory, and
// the chmod and chown after it followed the link — so a link planted
// at a d path under a world-writable /tmp (say /tmp/.X11-unix -> /etc)
// had root make the target 1777 at boot. For D the link can also be
// planted between the wipe and the mkdir.
func TestDirDoesNotChmodThroughASymlink(t *testing.T) {
	for _, kind := range []string{"d", "D"} {
		root := t.TempDir()
		victim := filepath.Join(root, "etc")
		if err := os.Mkdir(victim, 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "x11")
		if err := os.Symlink(victim, link); err != nil {
			t.Fatal(err)
		}
		e := entry{kind: kind, path: link, mode: 0o777, uid: -1, gid: -1}
		if kind == "D" {
			// Re-plant the link in the wipe→mkdir window, as a racing
			// attacker would; the hook runs right after RemoveAll.
			afterWipeHook = func() { os.Symlink(victim, link) }
		}
		err := apply(e)
		afterWipeHook = nil
		if err == nil {
			t.Errorf("%s on a symlink: no error, want a refusal", kind)
		}
		fi, err := os.Stat(victim)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("%s on a symlink changed its target %s from 0755 to %#o", kind, victim, fi.Mode().Perm())
		}
	}
}
