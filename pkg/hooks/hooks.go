// Package hooks implements the /etc/slinit/hooks.d/<point>/* mechanism:
// executable scripts dropped into a hook-point directory run at defined
// lifecycle events. finit-parity for the boot-order + shutdown-order
// scriptable extension points, minus finit's larger inventory of
// mount/plugin/network hooks that slinit either delegates to services
// or doesn't own.
//
// Contract:
//   - Scripts must be regular files with any execute bit set.
//   - Ordering: filepath.Glob then sort.Strings — same shell/systemd
//     tmpfiles.d convention (`00-x`, `10-y`, …).
//   - Each script gets 30 s to run before slinit gives up and continues;
//     a slow hook can't wedge boot / shutdown / switch-root.
//   - Non-zero exit is logged but never fatal — hooks are best-effort
//     augmentation, not gates. Operators who need a gate write a
//     regular service with `depends-on` instead.
//   - Environment: SLINIT_HOOK_POINT names the point (`system-up`,
//     `system-down`, `switch-root`), otherwise the calling env is
//     inherited so hooks can read the same PATH / env-file settings
//     the daemon has.
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

// hooksDir is the base directory containing per-point subdirectories.
// Overridable at package level for tests so they don't need to write
// to /etc.
var hooksDir = "/etc/slinit/hooks.d"

// perScriptTimeout is how long a single hook script may run before
// slinit kills it and moves on. 30 s matches systemd's
// TimeoutStartSec default and gives typical shell hooks (log rotate,
// stamp file, tmpfs prep) plenty of room without letting a runaway
// hook wedge PID 1.
var perScriptTimeout = 30 * time.Second

// SetHooksDir overrides the base hook-points directory. Only intended
// for tests + niche embedded builds that live outside /etc.
func SetHooksDir(path string) { hooksDir = path }

// SetPerScriptTimeout tunes the per-script kill window. Zero and
// negative fall back to the 30 s default.
func SetPerScriptTimeout(d time.Duration) {
	if d <= 0 {
		d = 30 * time.Second
	}
	perScriptTimeout = d
}

// Run executes every hook script under hooksDir/<point>/ in name-
// sorted order, one at a time (sequential, matching finit's
// plugin_run_hooks contract — parallelism here would race in
// operator-controlled scripts that typically expect ordered
// execution). Best-effort: returns the count of scripts run + any
// scripts that failed. Caller logs via `logger`; nothing here is
// fatal to slinit itself.
func Run(point string, logger *logging.Logger) (ran, failed int) {
	dir := filepath.Join(hooksDir, point)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("hooks: %s: %v", dir, err)
		}
		return 0, 0
	}
	// Sort by filename so 00-x runs before 10-y — same convention as
	// systemd tmpfiles.d and every other drop-in dir on Linux.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			logger.Warn("hooks: stat %s: %v", path, err)
			continue
		}
		if st.Mode()&0o111 == 0 {
			// Not executable — skip silently. Operator may have
			// dropped a README here or a WIP script without chmod.
			continue
		}
		ran++
		if err := runOne(point, path, logger); err != nil {
			logger.Warn("hooks: %s failed: %v", path, err)
			failed++
		}
	}
	return ran, failed
}

// runOne executes a single hook script with the per-script timeout.
func runOne(point, path string, logger *logging.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), perScriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(),
		"SLINIT_HOOK_POINT="+point,
	)
	// Route the script's stdout/stderr to whatever slinit's own
	// stdout/stderr point at (console under PID 1). Hooks that want
	// their own log go through the shell.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	logger.Info("hooks: running %s", path)
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout after %v", perScriptTimeout)
		}
		return err
	}
	return nil
}
