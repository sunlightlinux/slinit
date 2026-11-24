package service

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// countProcs counts live processes whose command line contains marker.
func countProcs(t *testing.T, marker string) int {
	t.Helper()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	n := 0
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err == nil && strings.Contains(string(b), marker) {
			if st, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat")); err == nil &&
				strings.Contains(string(st), ") Z ") {
				continue // a zombie is gone
			}
			n++
		}
	}
	return n
}

// With log-type = command, every start of the service spawned a new
// output-logger and overwrote s.loggerSup, but only a stop for good
// (BecomingInactive) stopped it: an auto-restart left the previous
// logger supervisor and its process behind, one more per restart.
func TestOutputLoggerNotLeakedAcrossRestarts(t *testing.T) {
	marker := "slinit-test-logger-" + strconv.Itoa(os.Getpid())
	set, _ := newTestSet()
	p := NewProcessService(set, "flaky")
	p.SetCommand([]string{"/bin/sh", "-c", "echo hi; sleep 0.2; exit 1"})
	p.SetLogType(LogToCommand)
	// A logger that ignores EOF on its input, as a real one may linger.
	p.SetOutputLogger([]string{"/bin/sh", "-c", "exec -a " + marker + " sleep 1000"})
	p.SetRestartDelay(50 * time.Millisecond)
	p.Record().SetAutoRestart(RestartAlways)
	set.AddService(p)
	set.StartService(p)
	t.Cleanup(func() {
		set.StopService(p)
		time.Sleep(200 * time.Millisecond)
	})

	time.Sleep(2 * time.Second) // several exit/restart cycles
	if n := countProcs(t, marker); n > 1 {
		t.Errorf("%d output-logger processes alive after restarts, want 1", n)
	}
}
