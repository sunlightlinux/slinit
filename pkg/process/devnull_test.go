package process

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The regular-file-only opens that keep a FIFO from blocking PID 1
// refused /dev/null as well, which every one of these readers used to
// take as empty: an env-file of /dev/null logged an error on every start
// (and stopped a user-mode slinit given it as --env-file), an env-dir
// entry symlinked to /dev/null - runit's way to unset a variable - was
// skipped so the variable stayed inherited, and load-credential from
// /dev/null failed the start.
func TestDevNullStillReadsEmpty(t *testing.T) {
	if m, err := ReadEnvFile(os.DevNull); err != nil || len(m) != 0 {
		t.Errorf("ReadEnvFile(/dev/null) = %v, %v; want empty, nil", m, err)
	}

	dir := t.TempDir()
	if err := os.Symlink(os.DevNull, filepath.Join(dir, "FOO")); err != nil {
		t.Fatal(err)
	}
	m, err := ReadEnvDir(dir)
	if v, ok := m["FOO"]; err != nil || !ok || v != "" {
		t.Errorf("env-dir entry -> /dev/null: FOO=%q present=%v err=%v; want present and empty (unset)", v, ok, err)
	}

	if b, err := readCredentialFile(os.DevNull); err != nil || len(b) != 0 {
		t.Errorf("readCredentialFile(/dev/null) = %q, %v; want empty, nil", b, err)
	}
}

// slinit's own --env-file is read once at start-up, outside any lock,
// so a pipe (`slinit -u --env-file <(...)`) is still read there.
func TestStartupEnvFileReadsPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		w.WriteString("A=1\n")
		w.Close()
	}()
	m, err := ReadStartupEnvFile(fmt.Sprintf("/proc/self/fd/%d", r.Fd()))
	if err != nil || m["A"] != "1" {
		t.Errorf("ReadStartupEnvFile(pipe) = %v, %v; want A=1", m, err)
	}
}
