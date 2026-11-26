package process

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestOpenConfiguredFileReadOnly opens an existing file with the
// read-only option and verifies fd behavior. Uses a temp file so the
// test doesn't rely on any host layout.
func TestOpenConfiguredFileReadOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openConfiguredFile(OpenFileEntry{Path: path, Options: "read-only"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	// A write should fail on a read-only handle.
	if _, err := f.Write([]byte("x")); err == nil {
		t.Errorf("write to read-only fd should fail")
	}
}

// TestOpenConfiguredFileGracefulMissing: with graceful=true, a
// non-existent file falls back to /dev/null (never errors).
func TestOpenConfiguredFileGracefulMissing(t *testing.T) {
	f, err := openConfiguredFile(OpenFileEntry{
		Path:    "/definitely/does/not/exist/anywhere/blah",
		Options: "graceful",
	})
	if err != nil {
		t.Fatalf("graceful should not error: %v", err)
	}
	defer f.Close()
	if f.Name() != "/dev/null" {
		t.Errorf("graceful fallback should be /dev/null, got %q", f.Name())
	}
}

// TestOpenConfiguredFileMissingErrors: without graceful, a missing
// path surfaces the open error.
func TestOpenConfiguredFileMissingErrors(t *testing.T) {
	_, err := openConfiguredFile(OpenFileEntry{
		Path:    "/nowhere/absent/path",
		Options: "read-only",
	})
	if err == nil {
		t.Errorf("missing read-only should error")
	}
}

// TestOpenConfiguredFileUnknownOption: unknown token surfaces as an
// error rather than being silently ignored.
func TestOpenConfiguredFileUnknownOption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x")
	os.WriteFile(path, nil, 0o644)
	if _, err := openConfiguredFile(OpenFileEntry{Path: path, Options: "typo-here"}); err == nil {
		t.Errorf("unknown option should error")
	}
}

// A FIFO is a legitimate OpenFile= target, but opening one read-only
// blocks until a writer appears, and this runs in PID 1. The open must
// return at once, and the fd the service gets must be blocking.
func TestOpenConfiguredFileFIFODoesNotBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	type res struct {
		f   *os.File
		err error
	}
	done := make(chan res, 1)
	go func() {
		f, err := openConfiguredFile(OpenFileEntry{Path: path, Options: "read-only"})
		done <- res{f, err}
	}()
	var r res
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		// Unblock the opener so the test can finish.
		if w, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
			w.Close()
		}
		if r = <-done; r.f != nil {
			r.f.Close()
		}
		t.Fatal("openConfiguredFile blocked on a FIFO")
	}
	if r.err != nil {
		t.Fatalf("open FIFO: %v", r.err)
	}
	defer r.f.Close()
	fl, err := unix.FcntlInt(r.f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if fl&unix.O_NONBLOCK != 0 {
		t.Errorf("FIFO fd handed to the service is O_NONBLOCK")
	}
}
