package recovery

import (
	"fmt"
	"io"
	"os"
	"time"
)

// CollapseAction is what PresentCollapse returns to the caller so
// the post-boot-collapse code path (all services stopped without an
// explicit shutdown) can dispatch. It's a separate enum from Action
// because the collapse menu offers different choices — no "drop to
// shell" here; instead you get "restart boot sequence" and "start
// recovery service", which are collapse-specific concepts (`s` and
// `e` in the old confirmRestartBoot prompt).
type CollapseAction int

const (
	// CollapseReboot: hard reboot via the shutdown executor.
	CollapseReboot CollapseAction = iota
	// CollapsePoweroff: hard poweroff via the shutdown executor.
	CollapsePoweroff
	// CollapseRestartBoot: caller should re-run tryStartServices on
	// the configured boot service list. The "continue trying" path
	// analogous to Action's ActionRetry — Ctrl-D aliases here.
	CollapseRestartBoot
	// CollapseRecoverySvc: caller should try to start the specific
	// service named "recovery". The escape-hatch path analogous to
	// Action's actionShell — Ctrl-B aliases here.
	CollapseRecoverySvc
	// CollapseTimeout: no input within the configured window;
	// caller should treat this as CollapseReboot (auto-reboot
	// safety net for headless systems).
	CollapseTimeout
)

// String makes CollapseAction self-describing in log lines.
func (a CollapseAction) String() string {
	switch a {
	case CollapseReboot:
		return "reboot"
	case CollapsePoweroff:
		return "poweroff"
	case CollapseRestartBoot:
		return "restart-boot"
	case CollapseRecoverySvc:
		return "recovery-svc"
	case CollapseTimeout:
		return "timeout(reboot)"
	default:
		return fmt.Sprintf("CollapseAction(%d)", int(a))
	}
}

// CollapseOptions tunes PresentCollapse behaviour. Zero-value gives
// sensible defaults (DefaultTimeout, /dev/console).
type CollapseOptions struct {
	// Timeout is the max time to wait for operator input before
	// auto-rebooting. Zero picks DefaultTimeout.
	Timeout time.Duration
	// ConsolePath is the tty to read/write. Empty selects
	// /dev/console (correct for PID 1 on system-mode boot).
	ConsolePath string
}

// PresentCollapse displays the boot-collapse rescue menu on
// /dev/console and returns the operator's chosen CollapseAction.
// Called from the "all services stopped without shutdown" fatal
// path in cmd/slinit/main.go — the same UX pattern as Present()
// (cbreak mode, tcflush stale input, single-keypress + Ctrl-B/
// Ctrl-D shortcuts, 60s auto-reboot safety net) applied to a
// different action set:
//
//	[r] reboot                       [p] poweroff
//	[s] restart boot sequence        [e] start recovery service
//	Ctrl-D → s (continue booting)    Ctrl-B → e (escape hatch)
//
// Ctrl-D/Ctrl-B pick the "continue" / "escape hatch" analogs from
// the load-fail menu so muscle memory carries between the two.
//
// Any I/O error opening the console falls back to CollapseTimeout
// so a truly headless system still auto-reboots (the design
// doesn't let a broken /dev/console strand PID 1).
func PresentCollapse(opts CollapseOptions) CollapseAction {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.ConsolePath == "" {
		opts.ConsolePath = "/dev/console"
	}
	tty, err := os.OpenFile(opts.ConsolePath, os.O_RDWR, 0)
	if err != nil {
		return CollapseTimeout
	}
	defer tty.Close()
	orig := setRawMode(tty)
	defer restoreTermios(tty, orig)
	return presentCollapse(tty, tty, opts.Timeout)
}

// presentCollapse is the testable core — takes explicit reader/
// writer so tests can pipe mock console I/O without touching
// /dev/console.
func presentCollapse(r io.Reader, w io.Writer, timeout time.Duration) CollapseAction {
	renderCollapseMenu(w, timeout)
	b, ok := readByteWithTimeout(r, w, timeout)
	if !ok {
		return CollapseTimeout
	}
	return collapseCharToAction(b)
}

// renderCollapseMenu writes the boxed menu to w. Same visual
// language as renderMenu (load-fail) so the two prompts feel like
// siblings, different action set.
func renderCollapseMenu(w io.Writer, timeout time.Duration) {
	writeBoxHeader(w, "slinit: BOOT COLLAPSE — all services stopped")
	writeBoxBlank(w)
	writeBoxLine(w, "Choose an action:")
	writeBoxLine(w, "  [r]  reboot now")
	writeBoxLine(w, "  [p]  power off")
	writeBoxLine(w, "  [s]  restart boot sequence           (Ctrl-D alias)")
	writeBoxLine(w, "  [e]  start recovery service          (Ctrl-B alias)")
	writeBoxBlank(w)
	writeBoxFooter(w, "reboot", timeout)
}

// collapseCharToAction maps the operator's single-char input to a
// CollapseAction. Literal letters (r/p/s/e) plus the Ctrl-B/Ctrl-D
// aliases — Ctrl-B → recovery svc (escape hatch), Ctrl-D → restart
// boot (continue).
func collapseCharToAction(b byte) CollapseAction {
	switch b {
	case 'r', 'R':
		return CollapseReboot
	case 'p', 'P':
		return CollapsePoweroff
	case 's', 'S', 0x04: // 0x04 = Ctrl-D → "continue booting"
		return CollapseRestartBoot
	case 'e', 'E', 0x02: // 0x02 = Ctrl-B → "escape hatch"
		return CollapseRecoverySvc
	default:
		// Unknown key → treat as timeout (safest — don't guess).
		return CollapseTimeout
	}
}
