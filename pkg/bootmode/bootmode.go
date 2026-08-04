// Package bootmode parses the kernel command line for slinit boot-time
// operator selectors (emergency / rescue / debug-shell / confirm-spawn /
// crash-shell / log-level). Centralises what was previously scattered
// kcmdlineHasFlag calls in cmd/slinit/main.go and lays the ground for
// systemd-parity operator features (Phases 2-5 of the recovery+boot
// refactor).
//
// Naming aligns with systemd's kernel-cmdline vocabulary
// (systemd.debug-shell, systemd.confirm_spawn, systemd.crash_shell,
// systemd.log_level) except the vendor prefix is slinit.* and the
// hyphen/underscore convention is normalised to hyphens throughout —
// matching the rest of slinit's cmdline surface (slinit.debug,
// slinit.rescue, slinit.emergency all pre-existed).
package bootmode

import (
	"os"
	"strings"
)

// Mode is the target boot mode: which service graph slinit activates
// after PID-1 initialisation. Emergency and Rescue skip the normal boot
// entirely and drop to sulogin, mirroring systemd's emergency.target /
// rescue.target semantics. The distinction between them is that Rescue
// mounts local filesystems first — Emergency does not, so it works even
// when the root FS mount is broken.
type Mode int

const (
	// Normal boot uses the configured boot service (default "boot",
	// overridable via -t / positional args). No emergency handling.
	Normal Mode = iota
	// Emergency boot: minimal path — sulogin on /dev/console, no
	// filesystems, no services. Escape hatch when root FS is corrupt
	// or a critical service is broken so early that even Rescue
	// cannot come up.
	Emergency
	// Rescue boot: like Emergency but with local filesystems mounted
	// so the operator can run fsck, edit /etc, etc. Sysvinit runlevel
	// 1 / "single" map here (dinit and sysvinit both treat "single"
	// as roughly the same thing).
	Rescue
)

// String returns the lowercase name of the mode, suitable for logging.
// Unknown Mode values fall back to "normal" — no reason to advertise
// an internal enum mismatch to the operator.
func (m Mode) String() string {
	switch m {
	case Emergency:
		return "emergency"
	case Rescue:
		return "rescue"
	default:
		return "normal"
	}
}

// Options captures every slinit-relevant setting parsed from the kernel
// command line. Zero values mean "no override requested" — main.go
// interprets them as "use flag/env default".
type Options struct {
	// Mode is the target boot mode. Emergency and Rescue skip the
	// normal service graph entirely.
	Mode Mode
	// DebugShell requests a persistent sulogin on a secondary VT
	// (default /dev/tty9), analog of systemd.debug-shell. Independent
	// of Mode — a Normal boot can still enable debug-shell for
	// post-boot access without pressing Ctrl-B at the right moment.
	DebugShell bool
	// ConfirmSpawn asks [y/n/skip] before each service is brought up
	// (systemd.confirm_spawn=yes). Slows boot dramatically — for
	// debugging service-order issues.
	ConfirmSpawn bool
	// CrashShell drops to sulogin instead of exiting on PID-1 panic
	// (systemd.crash_shell=1). Without it, PID-1 crash → kernel
	// panic (or reboot, depending on kernel policy).
	CrashShell bool
	// LogLevel overrides slinit's console log level when non-empty
	// (analog systemd.log_level=). One of debug / info / notice /
	// warn / error / critical / none — validated by the logger, not
	// here.
	LogLevel string
	// Debug is the legacy slinit.debug flag — verbose console
	// logging. Equivalent to LogLevel="debug" but kept separate so
	// existing operators' muscle memory keeps working.
	Debug bool
}

// Parse extracts slinit boot-mode settings from a raw kernel cmdline
// string (whitespace-separated tokens). Handles both bare tokens and
// KEY=VALUE forms; unknown tokens are silently ignored (the kernel
// forwards many of its own args through to PID 1). Last-mode-wins for
// conflicts: `emergency rescue` yields Rescue, matching systemd's
// precedence — the operator's most-recently-typed intent wins.
//
// Recognized tokens:
//
//	Bare:
//	  single, s, 1           — Mode = Rescue (sysvinit runlevel 1 compat)
//	  emergency              — Mode = Emergency
//	  rescue                 — Mode = Rescue
//	  slinit.emergency       — Mode = Emergency
//	  slinit.rescue          — Mode = Rescue
//	  slinit.debug-shell     — DebugShell = true
//	  slinit.confirm-spawn   — ConfirmSpawn = true
//	  slinit.crash-shell     — CrashShell = true
//	  slinit.debug           — Debug = true (verbose logging, legacy)
//	Key=value:
//	  slinit.log-level=<lvl> — LogLevel = <lvl>
func Parse(cmdline string) Options {
	var opts Options
	for _, tok := range strings.Fields(cmdline) {
		key, value, hasValue := tok, "", false
		if i := strings.IndexByte(tok, '='); i >= 0 {
			key = tok[:i]
			value = tok[i+1:]
			hasValue = true
		}
		// Bare-token selectors — any KEY=VALUE form is treated as
		// something else (kernel arg like root=/dev/sda1) and skipped
		// so a `single=1` misparse can't accidentally trip Rescue.
		if !hasValue {
			switch key {
			case "single", "s", "1":
				opts.Mode = Rescue
				continue
			case "emergency":
				opts.Mode = Emergency
				continue
			case "rescue":
				opts.Mode = Rescue
				continue
			}
		}
		switch key {
		case "slinit.emergency":
			opts.Mode = Emergency
		case "slinit.rescue":
			opts.Mode = Rescue
		case "slinit.debug-shell":
			opts.DebugShell = true
		case "slinit.confirm-spawn":
			opts.ConfirmSpawn = true
		case "slinit.crash-shell":
			opts.CrashShell = true
		case "slinit.debug":
			opts.Debug = true
		case "slinit.log-level":
			if hasValue {
				opts.LogLevel = value
			}
		}
	}
	return opts
}

// ParseFromProc reads /proc/cmdline and calls Parse. Returns zero-value
// Options + the read error on failure — callers typically log-and-continue
// (a missing /proc/cmdline means boot proceeds in Normal mode, which is
// the safe default).
func ParseFromProc() (Options, error) {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return Options{}, err
	}
	return Parse(string(data)), nil
}
