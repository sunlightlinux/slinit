package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunlightlinux/slinit/internal/safepath"
	"golang.org/x/sys/unix"
)

// A recursive Z must not follow a subdirectory that whoever owns the
// tree swaps for a symlink in the middle of the walk (CVE-2017-18078
// class). The walk used to Lstat each path and then chmod it by path:
// tree/sub/fN checked as a file in tree/sub was changed after tree/sub
// had become a link to outside, so root changed outside/fN. Here a
// goroutine exchanges tree/sub with tree/alt -> outside as fast as it
// can while Z runs over the tree; no file in outside may change mode.
func TestRecursiveChownSurvivesDirectorySwap(t *testing.T) {
	root := t.TempDir()
	tree, outside := filepath.Join(root, "tree"), filepath.Join(root, "outside")
	sub := filepath.Join(tree, "sub")
	for _, d := range []string{sub, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := range 64 {
			if err := os.WriteFile(filepath.Join(d, fmt.Sprint("f", i)), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	alt := filepath.Join(tree, "alt")
	if err := os.Symlink(outside, alt); err != nil {
		t.Fatal(err)
	}

	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			unix.Renameat2(unix.AT_FDCWD, sub, unix.AT_FDCWD, alt, unix.RENAME_EXCHANGE)
		}
	}()
	defer func() { close(stop); <-done }()

	e := entry{kind: "Z", path: tree, mode: 0o755, uid: -1, gid: -1}
	deadline := time.Now().Add(2 * time.Second)
	for runs := 0; time.Now().Before(deadline); runs++ {
		_ = applyChown(e, true)
		for i := range 64 {
			p := filepath.Join(outside, fmt.Sprint("f", i))
			if fi, err := os.Lstat(p); err == nil && fi.Mode().Perm() != 0o600 {
				t.Fatalf("run %d: Z on %s changed %s to %v through a swapped-in symlink", runs, tree, p, fi.Mode().Perm())
			}
		}
	}
}

// The same without a race: a symlinked directory already in the tree
// is skipped, not descended into.
func TestRecursiveChownSkipsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	tree, outside := filepath.Join(root, "tree"), filepath.Join(root, "outside")
	for _, d := range []string{tree, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := applyChown(entry{kind: "Z", path: tree, mode: 0o755, uid: -1, gid: -1}, true); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Errorf("Z followed tree/sub into %s: victim is %v", outside, fi.Mode().Perm())
	}
	if fi, _ := os.Stat(outside); fi.Mode().Perm() != 0o755 {
		t.Errorf("Z changed the symlinked directory %s to %v", outside, fi.Mode().Perm())
	}
}

// A hard link in the tree to a file outside it must not be changed
// while fs.protected_hardlinks is off: that link could be /etc/shadow.
// systemd-tmpfiles refuses the same (hardlink_vulnerable).
func TestRecursiveChownRefusesHardlink(t *testing.T) {
	saved := safepath.HardlinksProtected
	safepath.HardlinksProtected = func() bool { return false }
	t.Cleanup(func() { safepath.HardlinksProtected = saved })

	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim")
	if err := os.WriteFile(victim, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(victim, filepath.Join(tree, "hl")); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(tree, "plain")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := applyChown(entry{kind: "Z", path: tree, mode: 0o755, uid: -1, gid: -1}, true); err == nil {
		t.Error("Z over a hard link: no error, want a refusal")
	}
	if fi, _ := os.Stat(victim); fi.Mode().Perm() != 0o600 {
		t.Errorf("Z changed the hard-linked %s to %v", victim, fi.Mode().Perm())
	}
	// The refusal does not stop the rest of the tree.
	if fi, _ := os.Stat(plain); fi.Mode().Perm() != 0o755 {
		t.Errorf("Z skipped %s after the refusal: %v", plain, fi.Mode().Perm())
	}
}
