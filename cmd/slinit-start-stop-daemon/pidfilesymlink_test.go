package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A pid-file write must not follow a symlink.
//
// The path comes from --pidfile, and a pid-file's directory is
// routinely writable by the daemon's own user — that is how an
// unprivileged daemon rewrites its own pid-file. These tools run as
// root, so following a planted link turned the write into a
// truncate-to-a-pid-line of whatever it named.
//
// slinit's own code is the argument: pkg/process/pidfile.go opens
// these files O_NOFOLLOW to *read* them, noting that "services often
// write their pid-file into world-writable locations", and
// pkg/service/bgprocess.go calls the unguarded case "an arbitrary
// write primitive". These tools write the same files.
func TestWritePIDFileRefusesASymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "svc.pid")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	// The error is the point: refusing beats following. Whether the
	// caller treats it as fatal differs between the two tools, which is
	// why this asserts on the file rather than on the exit path.
	if err := writePIDFile(link, 4242); err == nil {
		t.Error("writePIDFile followed a symlink instead of refusing it")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "must survive" {
		t.Errorf("the symlink target was rewritten: %q", string(got))
	}
}

// And a real pid-file still works, including replacing a stale one —
// otherwise the guard would have broken the feature rather than scoped
// it.
func TestWritePIDFileStillWritesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svc.pid")

	if err := writePIDFile(path, 111); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writePIDFile(path, 2222); err != nil {
		t.Fatalf("replacing a stale pid-file: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "2222\n" {
		t.Errorf("pid-file holds %q; want the replacement, with no leftover of the old pid", string(b))
	}
}
