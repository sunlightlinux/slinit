//go:build paniconce

package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
)

// maybeArmPanicTimer is the ACTIVE implementation, compiled only
// when the `paniconce` build tag is set. Scans /proc/cmdline for
// `slinit.panic-after=<seconds>`; if present, spawns a goroutine
// that panics after that many seconds. Purpose: validate the
// slinit.crash-shell recovery path (`pkg/shutdown.CrashRecovery`
// with `spawnCrashShell`) end-to-end from a live boot without
// having to reach for a real bug.
//
// Never compiled into production slinit — the guarding build tag
// keeps this path out of the binary shipped by slpkgs template.
// The demo/build.sh sets `-tags paniconce` so the demo initramfs
// carries it and operators can `./demo/run.sh --panic-after=5
// --crash-shell` to see the crash-shell drop happen.
func maybeArmPanicTimer(logger *logging.Logger) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return
	}
	const prefix = "slinit.panic-after="
	for _, tok := range strings.Fields(string(data)) {
		if !strings.HasPrefix(tok, prefix) {
			continue
		}
		secStr := strings.TrimPrefix(tok, prefix)
		sec, err := strconv.Atoi(secStr)
		if err != nil || sec < 1 {
			logger.Warn("panic-after: invalid value %q (want positive integer seconds)", secStr)
			return
		}
		logger.Notice("panic-after: will PANIC in %ds (compiled with -tags paniconce)", sec)
		go func() {
			// A bare goroutine panic in Go crashes the whole process
			// — main's defer shutdown.CrashRecovery only sees main's
			// own stack. Wrap the test goroutine with its own defer
			// so the panic actually flows through the crash-shell
			// recovery path (proves the recovery UX works, not just
			// that panic → kernel-panic works).
			//
			// PID 1 branch is what we want here: kill-all + reboot
			// after operator exits the crash-shell. containerMode
			// stays false because slinit demo boots as PID 1, not
			// under Podman/Docker.
			defer shutdown.CrashRecovery(true, false)
			time.Sleep(time.Duration(sec) * time.Second)
			panic("panic-after: intentional test panic (slinit.panic-after)")
		}()
		return
	}
}
