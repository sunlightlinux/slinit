// Package hooks implements the /etc/slinit/hooks.d/<point>/* mechanism:
// executable scripts dropped into a hook-point directory run at defined
// lifecycle events. finit-parity for the boot-order + shutdown-order
// scriptable extension points, minus finit's larger inventory of
// mount/plugin/network hooks that slinit either delegates to services
// or doesn't own.
//
// Contract:
//   - Scripts must be regular files with any execute bit set.
//   - The hook-point directory and every script must be owned by root
//     and not writable by group or others, as sudo and cron require of
//     what they run as root; anything else is skipped with a warning.
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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/process"
)

// hooksDir is the base directory containing per-point subdirectories.
// Overridable at package level for tests so they don't need to write
// to /etc.
var hooksDir = "/etc/slinit/hooks.d"

// hookOwnerUID must own every hook directory and script that runs.
// Root; a variable so the tests can run unprivileged.
var hookOwnerUID uint32 = 0

// checkTrusted refuses what someone other than root could have
// written: an entry not owned by hookOwnerUID, or one writable by
// group or others. Every hook runs as root, so either would let
// that someone run code as root.
func checkTrusted(path string, info os.FileInfo) error {
	st, _ := info.Sys().(*syscall.Stat_t)
	if st == nil || st.Uid != hookOwnerUID {
		return fmt.Errorf("%s is not owned by uid %d; refusing it", path, hookOwnerUID)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s is writable by group or others (mode %#o); refusing it",
			path, info.Mode().Perm())
	}
	return nil
}

// checkTrustedDir Lstats dir and applies checkTrusted. A symlink is
// refused: its own mode is always 0777 and its target is not what
// was checked.
func checkTrustedDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory (a symlink is refused)", dir)
	}
	return checkTrusted(dir, info)
}

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
	if len(names) == 0 {
		return 0, 0
	}
	// Whoever can write the directory can replace any script in it
	// between this check and the exec, so it is checked too.
	if err := checkTrustedDir(dir); err != nil {
		logger.Warn("hooks: %v", err)
		return 0, 0
	}

	for _, name := range names {
		path := filepath.Join(dir, name)
		st, err := os.Lstat(path)
		if err != nil {
			logger.Warn("hooks: stat %s: %v", path, err)
			continue
		}
		if !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
			// Not executable — skip silently. Operator may have
			// dropped a README here or a WIP script without chmod.
			continue
		}
		if err := checkTrusted(path, st); err != nil {
			logger.Warn("hooks: %v", err)
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
// stdout+stderr are captured line-by-line through slinit's logger at
// Info level so the output doesn't race /dev/console with the
// interactive tty service's login prompt. Operators recover the
// output via `slinitctl catlog` or the journal — production console
// stays clean.
func runOne(point, path string, logger *logging.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), perScriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(),
		"SLINIT_HOOK_POINT="+point,
	)
	// Put the child in its own process group so a cmd.Cancel can kill
	// the whole group at timeout — the hung script's children with it.
	// Cancel fires only on timeout: whatever a script that exited on
	// its own left running is not touched.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			// Negative pid = kill entire process group.
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	logger.Info("hooks: running %s", path)
	drained, err := startLogged(cmd, "hook", point, path, logger)
	if err != nil {
		return err
	}
	err = process.WaitAdhoc(cmd)
	waitOutput(drained)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout after %v", perScriptTimeout)
		}
		// WaitAdhoc, not cmd.Wait: slinit-as-PID-1 generic-reaps
		// every zombie, and the window is widest exactly here —
		// a hook that pipes stdout/stderr makes Wait block on
		// drain. The router hands the real status back, so ECHILD
		// no longer stands in for a status we never got. It is kept
		// as a fallback: hooks are best-effort by contract (a
		// failure never gates boot), so absorbing it beats logging
		// a spurious "failed" warning.
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		return err
	}
	return nil
}

// outputGrace is how long a caller waits, after the script exits,
// for its output to reach EOF so the lines are logged before the
// "done" message. Only a background child still holding the pipe
// makes the wait last this long.
const outputGrace = 100 * time.Millisecond

// startLogged starts cmd with stdout+stderr on one pipe whose read
// end is drained, line by line into logger, until EOF — that is,
// until every writer has closed it, including any background child
// the script left running. The script's exit closes nothing under
// such a child: a closed read end would SIGPIPE a daemon started from
// rc.local on its next write. The drain goroutine (and the read fd)
// therefore lives as long as the last holder of the pipe; one per
// script run, so the cost is bounded by the number of scripts.
//
// The returned channel closes at EOF. The caller waits for the
// script itself (process.WaitAdhoc), not for the pipe.
func startLogged(cmd *exec.Cmd, kind, point, path string, logger *logging.Logger) (<-chan struct{}, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("output pipe: %w", err)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	err = cmd.Start()
	w.Close() // the child holds its own copy
	if err != nil {
		r.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		pipeLines(r, kind, point, path, logger)
	}()
	return done, nil
}

// waitOutput gives the output pipe outputGrace to reach EOF.
func waitOutput(drained <-chan struct{}) {
	select {
	case <-drained:
	case <-time.After(outputGrace):
	}
}

// pipeLines forwards every line r produces to `logger.Info` with a
// prefix that names the point + script path, matching pkg/network's
// existing pattern. It reads to EOF even past a line too long for
// the scanner, so no writer ever meets a closed pipe.
func pipeLines(r io.ReadCloser, kind, point, path string, logger *logging.Logger) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	base := filepath.Base(path)
	for sc.Scan() {
		logger.Info("%s[%s/%s]: %s", kind, point, base, sc.Text())
	}
	io.Copy(io.Discard, r)
}
