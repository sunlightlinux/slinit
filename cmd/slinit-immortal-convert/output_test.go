package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/internal/atomicfile/outputtest"
)

// TestMain runs main itself when re-executed by runMain.
func TestMain(m *testing.M) {
	if os.Getenv("SLINIT_CONVERT_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runMain runs the converter's main with args and returns its exit error.
func runMain(args ...string) error {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "SLINIT_CONVERT_RUN_MAIN=1")
	return cmd.Run()
}

// TestOutputDirWrites — --output-dir used os.WriteFile, which wrote
// through a symlink at the output name: out/NAME -> ../victim rewrote a
// file outside the directory (as root, into /etc/slinit.d). A symlink
// is now refused; a rerun still regenerates an existing file, now
// atomically.
func TestOutputDirWrites(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "foo.yml")
	os.WriteFile(in, []byte("cmd: /bin/true\n"), 0o644)
	outputtest.Check(t, runMain, in)
}
