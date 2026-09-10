package hooks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
)

// Legacy well-known paths honoured by SysV/Debian/Slackware/Alpine
// operators. Overridable at package level for tests.
var (
	rcLocalPath   = "/etc/rc.local"
	rcLocalDPath  = "/etc/rc.local.d"
	rcLocalTimeout = 5 * time.Minute
)

// SetRcLocalPath overrides the rc.local file path. Tests + niche
// embedded installs (busybox distros pointing at /etc/rc.d/rc.local).
func SetRcLocalPath(path string) { rcLocalPath = path }

// SetRcLocalDPath overrides the rc.local.d directory path.
func SetRcLocalDPath(path string) { rcLocalDPath = path }

// SetRcLocalTimeout overrides the per-script kill window for rc.local
// and its .d siblings. 5 min default gives large legacy init scripts
// (network config, licence import, backup replay) room without
// letting a runaway wedge the boot indefinitely.
func SetRcLocalTimeout(d time.Duration) {
	if d <= 0 {
		d = 5 * time.Minute
	}
	rcLocalTimeout = d
}

// RunRcLocal fires the legacy /etc/rc.local script + every executable
// under /etc/rc.local.d/ in name-sorted order. finit-parity for
// their runparts + rc.local documented in doc/runparts.md — a
// zero-config compat surface for SysV / Debian / Alpine / Slackware
// operators who ship a `/etc/rc.local` today. Same best-effort
// contract as Run() — missing files are silent no-ops; non-zero
// exits are logged but don't abort boot; per-script timeout kills a
// runaway.
//
// Callers: cmd/slinit main, from the OnBootReady wrapper right
// after hooks.Run("system-up"). Ordering matches Finit's contract
// (rc.local fires after the general system-up hook point) and gives
// operators layered control: modern hook drops in
// /etc/slinit/hooks.d/system-up/, legacy shell escape hatch in
// /etc/rc.local.
func RunRcLocal(logger *logging.Logger) (ran, failed int) {
	// /etc/rc.local.d/* first (matches Debian's rc-local.service
	// convention where drop-ins fire before the monolithic script).
	if entries, err := os.ReadDir(rcLocalDPath); err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, name := range names {
			path := filepath.Join(rcLocalDPath, name)
			if !isExecutableFile(path) {
				continue
			}
			ran++
			if err := runRcLocalScript(path, logger); err != nil {
				logger.Warn("rc.local.d: %s failed: %v", path, err)
				failed++
			}
		}
	} else if !os.IsNotExist(err) {
		logger.Warn("rc.local.d: %s: %v", rcLocalDPath, err)
	}

	// Then the classic monolithic script.
	if isExecutableFile(rcLocalPath) {
		ran++
		if err := runRcLocalScript(rcLocalPath, logger); err != nil {
			logger.Warn("rc.local: %s failed: %v", rcLocalPath, err)
			failed++
		}
	}
	return ran, failed
}

// isExecutableFile is true when path is a regular file with any
// execute bit set. Silent-skip for missing paths keeps the "no
// config needed" contract.
func isExecutableFile(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.Mode().IsRegular() && st.Mode()&0o111 != 0
}

// runRcLocalScript executes a single rc.local-shaped script under
// the long timeout window. Stdout/stderr inherit so operators see
// the classic "hello from rc.local" output on the console.
func runRcLocalScript(path string, logger *logging.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), rcLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(), "SLINIT_HOOK_POINT=rc-local")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logger.Info("rc.local: running %s", path)
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout after %v", rcLocalTimeout)
		}
		return err
	}
	return nil
}
