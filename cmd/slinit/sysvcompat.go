package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sunlightlinux/slinit/pkg/service"
)

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
			fmt.Printf("Usage: %s [-f] [-p|-P] [-r] [-h]\n", prog)
			fmt.Println("SysV compatibility shim — requests shutdown via the slinit control socket.")
			os.Exit(0)
		}
	}
	sendShutdownAndExit("", true, st)
	return true // unreachable
}
