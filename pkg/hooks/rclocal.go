package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
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
// the long timeout window. stdout+stderr are captured line-by-line
// through slinit's logger at Info level (same rationale as
// pkg/hooks/runOne — avoid racing /dev/console with the login
// prompt). `slinitctl catlog` and the journal preserve the output
// for post-hoc inspection.
func runRcLocalScript(path string, logger *logging.Logger) error {
	ctx, cancel := context.WithTimeout(context.Background(), rcLocalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(), "SLINIT_HOOK_POINT=rc-local")
	// Kill the whole process group on timeout so orphaned children
	// (typical: `network-restart` scripts that background dhclient)
	// don't hold the stdout pipe past the deadline. Matches
	// pkg/hooks/runOne — see rationale there.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	logger.Info("rc.local: running %s", path)
	if err := cmd.Start(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go pipeLines(&wg, stdout, "rc.local", "rc-local", path, logger)
	go pipeLines(&wg, stderr, "rc.local", "rc-local", path, logger)
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timeout after %v", rcLocalTimeout)
		}
		// Same PID-1-reaper race as pkg/hooks/runOne — see the
		// long comment there. ECHILD = child got reaped
		// generically; treat as success under the best-effort
		// contract.
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		return err
	}
	return nil
}
