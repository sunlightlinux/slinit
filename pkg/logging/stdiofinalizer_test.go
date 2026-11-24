package logging

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// StartCatchAll, ReattachStdoutErr and Stop each replaced os.Stdout and
// os.Stderr with a new *os.File for fd 1 and 2 and dropped the old one.
// A dropped *os.File closes its descriptor when the garbage collector
// finalizes it, so the next GC closed fd 1 and 2: under PID 1, the
// catch-all pipe every log line goes through. In this package's tests it
// took the test binary's stdout with it whenever a GC landed after them.
func TestCatchAllKeepsStdioOpenAcrossGC(t *testing.T) {
	saved1, err := unix.FcntlInt(1, unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	saved2, err := unix.FcntlInt(2, unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(saved1)
	defer unix.Close(saved2)

	cal, err := StartCatchAll(filepath.Join(t.TempDir(), "c.log"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cal.ReattachStdoutErr(); err != nil {
		t.Fatal(err)
	}
	cal.Stop()
	for i := 0; i < 5; i++ {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	var closed []int
	for _, fd := range []int{1, 2} {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			closed = append(closed, fd)
		}
	}
	// Put the test's own stdout/stderr back so the failure is seen.
	unix.Dup2(saved1, 1)
	unix.Dup2(saved2, 2)
	if len(closed) > 0 {
		t.Fatalf("fds %v were closed by the GC after the catch-all replaced os.Stdout/os.Stderr", closed)
	}
}
