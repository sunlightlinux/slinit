package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"
)

func openFDCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(ents)
}

// Each respawn opens the daemon's stdin, stdout and stderr (/dev/null,
// --stdout/--stderr files, or a logger pipe) and hands them to the
// child; exec.Cmd does not close a caller's *os.File, so the
// supervisor kept its copies until a garbage collection ran their
// finalizers. A crash-looping daemon grew the supervisor's descriptor
// table by three per respawn, and with a --stdout-logger the old
// logger never saw EOF and stayed running. Counted with the collector
// off, after each daemon has exited.
func TestStartDaemonClosesItsStdioCopies(t *testing.T) {
	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no true: %v", err)
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skipf("no cat: %v", err)
	}
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		opts Options
	}{
		{"devnull", Options{Service: "t", Exec: trueBin}},
		{"files", Options{Service: "t", Exec: trueBin,
			Stdin:  os.DevNull,
			Stdout: filepath.Join(dir, "out"), Stderr: filepath.Join(dir, "err")}},
		{"loggers", Options{Service: "t", Exec: trueBin,
			StdoutLogger: cat + " -", StderrLogger: cat + " -"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func() {
				_, exitCh, err := startDaemon(tc.opts)
				if err != nil {
					t.Fatalf("startDaemon: %v", err)
				}
				select {
				case <-exitCh:
				case <-time.After(5 * time.Second):
					t.Fatal("daemon did not exit")
				}
			}
			run()
			before := openFDCount(t)
			const rounds = 100
			for i := 0; i < rounds; i++ {
				run()
			}
			// A logger's read end is closed when the logger exits,
			// which it does once it sees EOF; give them a moment.
			var after int
			for deadline := time.Now().Add(5 * time.Second); ; {
				after = openFDCount(t)
				if after <= before+2 || time.Now().After(deadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if after > before+2 {
				t.Errorf("%d descriptors still open after %d respawns; want at most 2", after-before, rounds)
			}
		})
	}
}
