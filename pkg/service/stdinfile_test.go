package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// standard-input-file is launchd's StandardInPath: fd 0 reads from a
// file. It is the counterpart of logfile/stderr-logfile, which stdin
// had no form of.
func TestStandardInputFileFeedsStdin(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	out := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(in, []byte("line-one\nline-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewProcessService(set, "stdin-file")
	svc.SetCommand([]string{"/bin/sh", "-c", "cat > " + out})
	svc.Record().SetStandardInputFile(in)
	set.AddService(svc)
	set.StartService(svc)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && strings.Contains(string(b), "line-two") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	b, _ := os.ReadFile(out)
	t.Errorf("stdin did not come from the file; the child wrote %q", b)
}

// A missing file must fail the start. A service told to read a file
// that is not there should not come up reading /dev/null and look
// healthy.
func TestStandardInputFileMissingFailsTheStart(t *testing.T) {
	set, _ := newTestSet()
	svc := NewProcessService(set, "stdin-file-missing")
	svc.SetCommand([]string{"/bin/cat"})
	svc.Record().SetStandardInputFile(filepath.Join(t.TempDir(), "absent"))
	set.AddService(svc)
	set.StartService(svc)

	if got := waitState(t, svc, StateStopped, 5*time.Second); got != StateStopped {
		t.Errorf("service is %v; a missing input file should fail the start", got)
	}
}

// Without the directive nothing changes: close-stdin still routes fd 0
// to /dev/null rather than leaving it inherited.
func TestStandardInputFileUnsetLeavesCloseStdinAlone(t *testing.T) {
	set, _ := newTestSet()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.txt")
	svc := NewProcessService(set, "stdin-closed")
	svc.SetCommand([]string{"/bin/sh", "-c", "cat > " + out + " 2>&1"})
	svc.SetCloseFDs(true, false, false)
	set.AddService(svc)
	set.StartService(svc)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(out); err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	b, _ := os.ReadFile(out)
	if len(b) != 0 {
		t.Errorf("close-stdin produced input: %q", b)
	}
}
