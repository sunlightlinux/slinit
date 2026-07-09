package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sunlightlinux/slinit/pkg/logging"
	"github.com/sunlightlinux/slinit/pkg/service"
	"github.com/sunlightlinux/slinit/pkg/shutdown"
	"github.com/sunlightlinux/slinit/pkg/utmp"
)

// sysvExtraFlags captures the systemd(1) reboot/poweroff/halt options
// that change *how* the shutdown is performed but not *what* it is.
// Kept separate from parseSysVCompat's shutdown-type mapping so the
// two concerns can be tested independently.
type sysvExtraFlags struct {
	force    bool // -f / --force     — bypass the daemon (direct kernel reboot)
	wtmpOnly bool // -w / --wtmp-only — write shutdown record and exit
	noWtmp   bool // -d / --no-wtmp   — skip utmp/wtmp shutdown record
	noSync   bool // -n / --no-sync   — skip filesystem sync
	noWall   bool // --no-wall        — skip wall broadcast
}

// parseSysVExtraFlags scans argv[1:] for the systemd(1) reboot/halt/
// poweroff flags that gate side-effects (or, for -f, choose a completely
// different execution path). Unrecognised args are ignored to match the
// historical "silently tolerated" contract of legacy SysV shims.
func parseSysVExtraFlags(argv []string) sysvExtraFlags {
	var f sysvExtraFlags
	if len(argv) <= 1 {
		return f
	}
	for _, a := range argv[1:] {
		switch a {
		case "-f", "--force":
			f.force = true
		case "-w", "--wtmp-only":
			f.wtmpOnly = true
		case "-d", "--no-wtmp":
			f.noWtmp = true
		case "-n", "--no-sync":
			f.noSync = true
		case "--no-wall":
			f.noWall = true
		}
	}
	return f
}

// parseSysVCompat inspects argv[0] and the argument list to decide
// whether this invocation is a SysV init compatibility call (i.e. slinit
// was run through one of the /sbin/halt, /sbin/poweroff, /sbin/reboot
// compat symlinks) and, if so, what ShutdownType it maps to.
//
// Flag overrides follow busybox/sysvinit conventions:
//   - `halt -p` or `halt -P` → poweroff (instead of plain halt)
//   - `poweroff -r` → reboot
//   - `reboot -p` → poweroff
//   - `-h` is accepted as "halt" for halt/poweroff, ignored for reboot
//
// Unknown flags (e.g. `-f`, `-n`, `-w`, `--no-wall`) are tolerated
// silently so legacy scripts continue to work. Returns ok=false if
// argv[0] is not one of the recognised compat names.
func parseSysVCompat(argv []string) (st service.ShutdownType, prog string, ok bool) {
	if len(argv) == 0 {
		return 0, "", false
	}
	prog = filepath.Base(argv[0])

	switch prog {
	case "halt":
		st = service.ShutdownHalt
	case "poweroff":
		st = service.ShutdownPoweroff
	case "reboot":
		st = service.ShutdownReboot
	default:
		return 0, prog, false
	}

	for _, a := range argv[1:] {
		switch a {
		case "-p", "-P":
			st = service.ShutdownPoweroff
		case "-r":
			st = service.ShutdownReboot
		case "-h":
			// shutdown-style -h means halt; harmless for halt itself,
			// but we don't let it override an explicit `reboot` call.
			if prog != "reboot" {
				st = service.ShutdownHalt
			}
		}
	}
	return st, prog, true
}

// handleSysVCompat dispatches argv[0]-based SysV init compatibility
// when slinit was invoked as /sbin/halt, /sbin/poweroff or /sbin/reboot.
// It connects to the running system-mode control socket and requests
// the mapped shutdown, then exits.
//
// Skipped when running as PID 1: if slinit itself is init, a compat
// name in argv[0] is either a misconfiguration or the kernel handing
// down an argv we should not act on, and the correct behaviour is to
// fall through to normal boot-time parsing.
//
// Returns true only if dispatch handled the invocation (in practice
// unreachable because sendShutdownAndExit calls os.Exit).
func handleSysVCompat() bool {
	if os.Getpid() == 1 {
		return false
	}
	st, prog, ok := parseSysVCompat(os.Args)
	if !ok {
		return false
	}
	for _, a := range os.Args[1:] {
		if a == "--help" || a == "-?" {
			fmt.Printf("Usage: %s [-f] [-p|-P] [-r] [-h] [-n] [-w] [-d] [--no-wall]\n", prog)
			fmt.Println("SysV compatibility shim — requests shutdown via the slinit control socket.")
			fmt.Println("With -f/--force, bypasses the daemon and reboots the kernel directly")
			fmt.Println("(matches systemd's reboot(8) contract).")
			os.Exit(0)
		}
	}

	extra := parseSysVExtraFlags(os.Args)

	// -w / --wtmp-only: write the shutdown record and exit without
	// touching the init system or the reboot syscall. Same contract as
	// systemd's reboot(8) -w. Wins over -f: matches slinit-shutdown's
	// argument-precedence order.
	if extra.wtmpOnly {
		utmp.LogShutdown()
		os.Exit(0)
	}

	// -f / --force: bypass the daemon and perform the shutdown directly
	// — kill(-1), umount, sync, reboot syscall — instead of asking init
	// to stop services one by one. Matches systemd's reboot -f (which
	// documents "does not contact the init system"). Without -f, we
	// preserve the historical behaviour of sending a graceful shutdown
	// request to the running slinit instance.
	if extra.force {
		if extra.noSync {
			shutdown.SetSyncEnabled(false)
		}
		if extra.noWtmp {
			shutdown.SetWtmpEnabled(false)
		}
		if extra.noWall {
			shutdown.SetWallEnabled(false)
		}
		doForceShutdownAndExit(st)
	}

	sendShutdownAndExit("", true, st)
	return true // unreachable
}

// doForceShutdownAndExit runs the minimal `reboot -f` path — sync and
// reboot syscall, no service teardown, no umount. Matches systemd's
// documented contract: "does not contact the init system" and
// "filesystems are not properly unmounted before shutdown". If the
// reboot syscall unexpectedly returns, we exit 1 so the caller notices
// something went wrong.
func doForceShutdownAndExit(st service.ShutdownType) {
	logger := logging.New(logging.LevelInfo)
	if f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
		logger.SetOutput(f)
	}
	shutdown.ExecuteForce(st, logger)
	os.Exit(1)
}
