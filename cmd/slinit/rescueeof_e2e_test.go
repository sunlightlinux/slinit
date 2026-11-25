package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// With no boot service and a console that only returns EOF (/dev/null
// bound on /dev/console), every EOF was taken for Ctrl-D: the rescue
// menu retried, failed and was presented again at once, forever —
// ~700 KB of log in 4 s, and the 60 s auto-reboot never came.
func TestRescueMenuDeadConsoleDoesNotLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sd"), 0755); err != nil {
		t.Fatal(err)
	}
	startNS(t, dir, `
mount --bind /dev/null /dev/console && mount -t tmpfs none /run
exec "$B/slinit" -d "$D/sd" -p "$D/sock" --syslog-socket "" --no-watchdog --catch-all-log="$D/cal"`)
	time.Sleep(2 * time.Second)
	b, _ := os.ReadFile(filepath.Join(dir, "cal"))
	if n := strings.Count(string(b), "entering rescue menu"); n == 0 || n > 2 {
		t.Errorf("rescue menu entered %d times in 2s (log %d bytes); want 1 or 2", n, len(b))
	}
}
