package shutdown

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"golang.org/x/sys/unix"
)

// The clock timestamp must be written while the filesystems are still
// writable. Execute wrote it after unmountAll, whose last step remounts
// / read-only, so on a normal shutdown the write failed (logged at debug
// level) and the next boot's clock guard started from a stale time.
func TestExecuteWritesClockBeforeRootGoesReadOnly(t *testing.T) {
	dir := t.TempDir()
	clock := filepath.Join(dir, "clock")
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	origClock, origMounts, origSwaps, origMount := clockTimestampPath, unmountProcPath, swapsProcPath, mountFunc
	defer func() {
		clockTimestampPath, unmountProcPath, swapsProcPath, mountFunc = origClock, origMounts, origSwaps, origMount
	}()

	clockAtRemount := false
	remounted := false
	runExecuteWithMocksHook(t, func() {
		clockTimestampPath = clock
		unmountProcPath = empty
		swapsProcPath = empty
		runHookFunc = func(service.ShutdownType, *logging.Logger) bool { return false }
		mountFunc = func(src, target, fstype string, flags uintptr, data string) error {
			if target == "/" && flags&unix.MS_RDONLY != 0 {
				remounted = true
				_, err := os.Stat(clock)
				clockAtRemount = err == nil
			}
			return nil
		}
	})
	if !remounted {
		t.Fatal("unmountAll did not remount / read-only")
	}
	if !clockAtRemount {
		t.Error("clock timestamp was not written before / was remounted read-only")
	}
}
