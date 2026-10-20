package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A service directory that is a symlink must be refused, not followed.
//
// os.MkdirAll short-circuits on an existing path through os.Stat, which
// follows a symlink, so a standing symlink-to-directory makes MkdirAll
// return nil — and the chmod/chown that follow it then act on the
// target, as root. No race is needed: service directory names may have
// several components (parseServiceDirNames permits "myapp/data"), the
// first start chowns "myapp" to the service user, and from then on that
// user owns the parent of the leaf.
func TestServiceDirRefusesASymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "statedir")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	err := ensureServiceDirs([]ServiceDir{{Path: link, Mode: 0o777}}, 0, 0)
	if err == nil {
		t.Error("ensureServiceDirs accepted a symlink as a service directory")
	} else if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("error does not say why it refused: %v", err)
	}

	fi, statErr := os.Lstat(target)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("the symlink target was re-moded to %v; want 0700 untouched", fi.Mode().Perm())
	}
}

// And the ordinary cases still work, or the guard would have broken the
// directive rather than scoped it: a fresh directory is created with
// the requested mode, and an existing real one has its mode corrected.
func TestServiceDirStillCreatesAndCorrects(t *testing.T) {
	root := t.TempDir()

	fresh := filepath.Join(root, "fresh", "nested")
	if err := ensureServiceDirs([]ServiceDir{{Path: fresh, Mode: 0o750}}, 0, 0); err != nil {
		t.Fatalf("creating a fresh nested directory: %v", err)
	}
	if fi, err := os.Lstat(fresh); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o750 {
		t.Errorf("fresh directory has mode %v; want 0750", fi.Mode().Perm())
	}

	// Existing and deliberately wrong, which is the case the doc
	// comment calls out: "an existing directory is left in place but
	// its mode/owner are corrected".
	stale := filepath.Join(root, "stale")
	if err := os.Mkdir(stale, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := ensureServiceDirs([]ServiceDir{{Path: stale, Mode: 0o700}}, 0, 0); err != nil {
		t.Fatalf("correcting an existing directory: %v", err)
	}
	if fi, err := os.Lstat(stale); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o700 {
		t.Errorf("existing directory has mode %v; want it corrected to 0700", fi.Mode().Perm())
	}
}
