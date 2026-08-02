package recovery

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// DebugAction is what the debug menu returns after operator input.
// Distinct from Action / CollapseAction because the debugger has
// its own action set (continue-boot, force-fail-current-service)
// that neither of the boot-failure menus need.
type DebugAction int

const (
	// DebugContinue: operator dismissed the menu; reader resumes
	// listening for the next Ctrl-B. Boot progression is unaffected
	// (see PresentDebug docs for the honest "we don't actually
	// freeze the state machine" note).
	DebugContinue DebugAction = iota
	// DebugReboot: hard reboot via caller-supplied RebootFn.
	DebugReboot
	// DebugPoweroff: hard poweroff via caller-supplied PoweroffFn.
	DebugPoweroff
	// DebugForceFail: mark the first in-progress service as failed
	// via caller-supplied ForceFailFn. Used to skip a stuck
	// dependency without a full reboot.
	DebugForceFail
	// DebugTimeout: no input before deadline; treated like
	// DebugContinue (menu auto-dismisses).
	DebugTimeout
)

// String makes DebugAction self-describing in log lines.
func (a DebugAction) String() string {
	switch a {
	case DebugContinue:
		return "continue"
	case DebugReboot:
		return "reboot"
	case DebugPoweroff:
		return "poweroff"
	case DebugForceFail:
		return "force-fail"
	case DebugTimeout:
		return "timeout(continue)"
	default:
		return fmt.Sprintf("DebugAction(%d)", int(a))
	}
}

// debugActionShell is the internal sentinel for "operator picked
// shell". PresentDebug handles the fork inline and re-loops, so
// callers never see it in the returned DebugAction.
const debugActionShell DebugAction = -1

// ServiceInfo describes one service for the debug status table.
// Kept string-typed so pkg/recovery doesn't have to import
// pkg/service (would risk an import cycle with future refactors).
type ServiceInfo struct {
	// Name of the service.
	Name string
	// State is the human-readable state ("STARTING", "STOPPED",
	// etc). Comes from ServiceState.String() at the call site.
	State string
	// Note is a free-form annotation shown after the state — e.g.
	// "waiting for X", "STARTING for 4.2s", "PID=1234". Optional.
	Note string
}

// StatusSnapshot is the caller-supplied view of what services are
// currently in flight, refreshed each time the debug menu opens.
// The caller (main.go) computes this from ServiceSet state — the
// debugger asks via StatusFn rather than reaching into pkg/service
// directly, keeping the two decoupled.
type StatusSnapshot struct {
	// Elapsed is time since Debugger.Start (i.e., roughly boot time
	// so far). Shown in the menu header so the operator sees "how
	// long has boot been running".
	Elapsed time.Duration
	// InProgress are services actively transitioning (STARTING /
	// STOPPING). Force-fail targets the first entry.
	InProgress []ServiceInfo
	// Waiting are services that are loaded but haven't been
	// activated yet (blocked on deps, triggered, path-activated).
	Waiting []ServiceInfo
	// RecentErrors is a small ring-buffer of recent error messages
	// the caller has logged. Truncated to fit the menu box.
	RecentErrors []string
}

// DebuggerLogger is the minimal logger interface Debugger needs.
// A subset of logging.Logger — kept small so tests can pass a
// stub without dragging in the real logger.
type DebuggerLogger interface {
	Info(format string, args ...interface{})
	Notice(format string, args ...interface{})
	Warn(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// DebuggerOptions wires the debugger to the caller-supplied
// facilities. All function-typed fields are called from a single
// goroutine (the reader) so they don't need to be reentrant, but
// they may run concurrently with other slinit code so they must
// be individually safe.
type DebuggerOptions struct {
	// ConsolePath is the tty the reader owns. Empty selects
	// /dev/console.
	ConsolePath string
	// Timeout is how long the menu waits for operator input before
	// auto-dismissing (DebugTimeout, treated like Continue). Zero
	// picks DefaultTimeout.
	Timeout time.Duration
	// StatusFn returns a live snapshot of ServiceSet state each
	// time the menu opens. Required.
	StatusFn func() StatusSnapshot
	// ForceFailFn is called with the name of the first in-progress
	// service when the operator picks [f]. Should invoke
	// serviceSet.ForceStopService or equivalent.
	ForceFailFn func(name string) error
	// RebootFn is called when the operator picks [r]. Typically
	// invokes shutdown.Execute(ShutdownReboot, logger) and does
	// not return.
	RebootFn func()
	// PoweroffFn is called when the operator picks [p]. Typically
	// invokes shutdown.Execute(ShutdownPoweroff, logger) and does
	// not return.
	PoweroffFn func()
	// Logger receives info/warn/error messages for debugger
	// lifecycle events (menu opened, action chosen, force-fail
	// result). Optional — nil disables logging.
	Logger DebuggerLogger
	// ShellCandidates overrides the default shell search list
	// (sulogin → /bin/sh). Empty picks the same defaults as
	// pkg/recovery.Present.
	ShellCandidates []string
}

// Debugger owns a goroutine that continuously reads /dev/console in
// raw mode and pops the debug menu on Ctrl-B (0x02). One instance
// per boot; start it in main.go's PID-1 branch after ServiceSet is
// ready, stop it when a console-owning service (getty on the tty
// service) is about to start.
//
// Honest note on "pause": the debugger DOES NOT freeze slinit's
// event loop while the menu is open — that would deadlock the
// watchdog feeder + control-socket accept + signal handling. What
// it does is present a live-status view: the state machine keeps
// running underneath, and the snapshot is re-computed each time
// the menu re-renders. Force-fail is the only action that mutates
// state; continue/reboot/poweroff/shell are read-only from
// ServiceSet's perspective.
type Debugger struct {
	opts      DebuggerOptions
	startTime time.Time
	stopCh    chan struct{}
	doneCh    chan struct{}
	tty       *os.File
	// termios is the ORIGINAL termios captured by Start, so Stop
	// (and the shell-fork detour in runShell) can restore it.
	// Guarded by termiosMu because runShell re-captures on
	// re-arm; the read from Stop must see the freshest pointer.
	termiosMu sync.Mutex
	termios   *unix.Termios
	menuMu    sync.Mutex // held while the menu is presenting
	running   atomic.Bool
}

// NewDebugger fills defaults and returns a debugger that hasn't
// started yet — call Start to begin the reader goroutine.
func NewDebugger(opts DebuggerOptions) *Debugger {
	if opts.ConsolePath == "" {
		opts.ConsolePath = "/dev/console"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if len(opts.ShellCandidates) == 0 {
		opts.ShellCandidates = defaultShellCandidates
	}
	return &Debugger{opts: opts}
}

// Start opens the console, sets raw mode + tcflush, and spawns the
// reader goroutine. Returns error if the console can't be opened
// (headless system, permission problem); the caller should log
// and continue — a missing debugger doesn't block boot.
//
// Safe to call once. A second Start on the same Debugger returns
// nil without side effects.
func (d *Debugger) Start() error {
	if !d.running.CompareAndSwap(false, true) {
		return nil
	}
	d.startTime = time.Now()
	d.stopCh = make(chan struct{})
	d.doneCh = make(chan struct{})
	tty, err := os.OpenFile(d.opts.ConsolePath, os.O_RDWR, 0)
	if err != nil {
		d.running.Store(false)
		close(d.doneCh)
		return fmt.Errorf("open console %q: %w", d.opts.ConsolePath, err)
	}
	d.tty = tty
	orig := setRawMode(tty)
	d.termiosMu.Lock()
	d.termios = orig
	d.termiosMu.Unlock()
	go d.run()
	if d.opts.Logger != nil {
		d.opts.Logger.Info("Boot debugger active on %s (press Ctrl-B for menu)", d.opts.ConsolePath)
	}
	return nil
}

// Stop signals the reader goroutine to exit, waits for it, restores
// the original termios, and closes the console fd. Safe to call
// multiple times; second and subsequent calls are no-ops. Safe to
// call from any goroutine.
//
// Typical wiring: register as a ServiceListener on the boot service
// and call Stop from EventStarted — that's the point where getty
// on the tty service has taken /dev/console and further Ctrl-B
// reads would compete with the login prompt.
func (d *Debugger) Stop() {
	if !d.running.CompareAndSwap(true, false) {
		return
	}
	close(d.stopCh)
	// Closing the tty fd unblocks the goroutine's ReadByte via
	// EBADF/EIO — the standard way to unstick a blocking read on
	// a file with no deadline support.
	if d.tty != nil {
		d.tty.Close()
	}
	<-d.doneCh
	// Restore termios AFTER goroutine exit — otherwise a race
	// window could see the goroutine reading in the wrong mode.
	d.termiosMu.Lock()
	orig := d.termios
	d.termios = nil
	d.termiosMu.Unlock()
	restoreTermios(d.tty, orig)
	if d.opts.Logger != nil {
		d.opts.Logger.Info("Boot debugger stopped")
	}
}

// run is the reader loop. Byte-at-a-time in raw mode, filters for
// Ctrl-B (0x02), opens the menu on hit. Every other byte is
// silently dropped — a stray keypress at boot time shouldn't
// trigger anything, and letting bytes accumulate on getty would
// break the eventual login flow (mitigated by Stop before getty).
func (d *Debugger) run() {
	defer close(d.doneCh)
	br := bufio.NewReader(d.tty)
	for {
		select {
		case <-d.stopCh:
			return
		default:
		}
		b, err := br.ReadByte()
		if err != nil {
			// Console closed (Stop) or transient error → exit.
			// Better to fail closed than spam CPU on a broken fd.
			return
		}
		if b != 0x02 { // only Ctrl-B triggers the menu
			continue
		}
		// Serialize with itself — the menu presentation is not
		// reentrant. A second Ctrl-B while the menu is open would
		// interleave input, so we hold menuMu across the whole
		// present-and-dispatch cycle.
		d.menuMu.Lock()
		action := d.presentMenu()
		d.menuMu.Unlock()
		d.dispatch(action)
	}
}

// presentMenu is the outer menu loop: render + read + handle-shell.
// Returns when the operator picks a non-shell action (or menu times
// out). Runs on the reader goroutine; the tty is already in raw
// mode from Start.
func (d *Debugger) presentMenu() DebugAction {
	// Fresh snapshot per menu open — state may have advanced since
	// the previous Ctrl-B (or the state machine may have made
	// progress while the menu was open a moment ago).
	for {
		snap := d.opts.StatusFn()
		snap.Elapsed = time.Since(d.startTime)
		renderDebugMenu(d.tty, snap, d.opts.Timeout)
		b, ok := readByteWithTimeout(d.tty, d.tty, d.opts.Timeout)
		if !ok {
			return DebugTimeout
		}
		action := debugCharToAction(b)
		if action != debugActionShell {
			return action
		}
		// Shell: temporarily restore canonical mode, fork the shell,
		// re-arm raw mode on return, then loop back to re-present
		// the menu (operator may have fixed something and wants
		// another look at status).
		d.runShell()
	}
}

// runShell drops into a shell with the same UX contract as the
// load-fail menu's shell action — canonical mode restored so the
// shell has normal line editing, re-armed to raw on return. When
// the ORIGINAL termios wasn't captured (fd wasn't a tty) the
// shell runs directly and the "restore then re-set" dance is a
// no-op, matching Present's behaviour.
func (d *Debugger) runShell() {
	runCanonical := func(fn func()) {
		d.termiosMu.Lock()
		orig := d.termios
		d.termiosMu.Unlock()
		if orig == nil {
			fn()
			return
		}
		restoreTermios(d.tty, orig)
		fn()
		newOrig := setRawMode(d.tty)
		d.termiosMu.Lock()
		d.termios = newOrig
		d.termiosMu.Unlock()
	}
	forkShellOnConsole(d.tty, d.opts.ShellCandidates, d.opts.ConsolePath, runCanonical)
}

// dispatch executes the operator's chosen action. Reboot/Poweroff
// invoke caller callbacks that never return; Continue/Timeout drop
// back to the reader loop; ForceFail mutates ServiceSet via the
// caller's ForceFailFn.
func (d *Debugger) dispatch(a DebugAction) {
	log := d.opts.Logger
	switch a {
	case DebugContinue, DebugTimeout:
		if log != nil {
			log.Info("Debug menu: %s", a)
		}
	case DebugReboot:
		if log != nil {
			log.Notice("Debug menu: user chose reboot")
		}
		if d.opts.RebootFn != nil {
			d.opts.RebootFn() // does not return
		}
	case DebugPoweroff:
		if log != nil {
			log.Notice("Debug menu: user chose poweroff")
		}
		if d.opts.PoweroffFn != nil {
			d.opts.PoweroffFn() // does not return
		}
	case DebugForceFail:
		snap := d.opts.StatusFn()
		if len(snap.InProgress) == 0 {
			if log != nil {
				log.Warn("Debug menu: force-fail requested but no service in progress")
			}
			fmt.Fprintf(d.tty, "\n[debug] no service in progress — nothing to force-fail\n")
			return
		}
		target := snap.InProgress[0].Name
		if d.opts.ForceFailFn == nil {
			if log != nil {
				log.Warn("Debug menu: force-fail requested but no ForceFailFn wired")
			}
			return
		}
		if err := d.opts.ForceFailFn(target); err != nil {
			if log != nil {
				log.Error("Debug menu: force-fail %q failed: %v", target, err)
			}
			fmt.Fprintf(d.tty, "\n[debug] force-fail %q failed: %v\n", target, err)
			return
		}
		if log != nil {
			log.Notice("Debug menu: force-failed service %q", target)
		}
		fmt.Fprintf(d.tty, "\n[debug] force-failed %q\n", target)
	}
}

// renderDebugMenu writes the boxed debug menu on w. Uses the same
// visual language as renderMenu (load-fail) and renderCollapseMenu
// so the three boot-failure prompts feel like siblings. Truncates
// service names + notes so the box stays visually intact on
// 80-col serial consoles.
func renderDebugMenu(w io.Writer, snap StatusSnapshot, timeout time.Duration) {
	const bar = "+============================================================+"
	fmt.Fprintf(w, "\n%s\n", bar)
	fmt.Fprintf(w, "| slinit: BOOT DEBUGGER — Ctrl-B intercepted at %6.2fs      |\n", snap.Elapsed.Seconds())
	renderServiceBlock(w, "In progress", snap.InProgress, 5)
	renderServiceBlock(w, "Waiting on deps", snap.Waiting, 5)
	renderErrorBlock(w, snap.RecentErrors)
	fmt.Fprintf(w, "|                                                            |\n")
	fmt.Fprintf(w, "| Actions:                                                   |\n")
	fmt.Fprintf(w, "|   [c] / Ctrl-D   continue boot                             |\n")
	fmt.Fprintf(w, "|   [s] / Ctrl-B   drop to shell                             |\n")
	fmt.Fprintf(w, "|   [f]            force-fail first in-progress service      |\n")
	fmt.Fprintf(w, "|   [r]            reboot           [p]  power off           |\n")
	fmt.Fprintf(w, "|                                                            |\n")
	fmt.Fprintf(w, "| Auto-continue in %2ds if no input.                          |\n", int(timeout.Seconds()))
	fmt.Fprintf(w, "%s\n> ", bar)
}

// renderServiceBlock prints a section like:
//
//	|                                                            |
//	| In progress (2):                                           |
//	|   healthcheck-demo    STARTING for 4.2s                    |
//	|   ...                                                      |
//
// max caps the number of shown entries; overflow gets a "+N more"
// tail so the operator knows they're seeing a truncated view.
// Silently omits the whole block when svcs is empty.
func renderServiceBlock(w io.Writer, title string, svcs []ServiceInfo, max int) {
	if len(svcs) == 0 {
		return
	}
	fmt.Fprintf(w, "|                                                            |\n")
	fmt.Fprintf(w, "| %-58s |\n", fmt.Sprintf("%s (%d):", title, len(svcs)))
	shown := svcs
	if len(shown) > max {
		shown = shown[:max]
	}
	for _, s := range shown {
		line := fmt.Sprintf("  %-20s %s", truncString(s.Name, 20), s.State)
		if s.Note != "" {
			line = fmt.Sprintf("  %-20s %s (%s)", truncString(s.Name, 20), s.State, s.Note)
		}
		if len(line) > 58 {
			line = line[:55] + "..."
		}
		fmt.Fprintf(w, "| %-58s |\n", line)
	}
	if len(svcs) > max {
		fmt.Fprintf(w, "| %-58s |\n", fmt.Sprintf("  ... +%d more", len(svcs)-max))
	}
}

// renderErrorBlock prints the "Recent errors" section, mirroring
// renderServiceBlock's shape. Cap at 3 lines to keep the menu
// tight; full history is in the log.
func renderErrorBlock(w io.Writer, errs []string) {
	if len(errs) == 0 {
		return
	}
	fmt.Fprintf(w, "|                                                            |\n")
	fmt.Fprintf(w, "| Recent errors (last %d):                                   |\n", len(errs))
	shown := errs
	const max = 3
	if len(shown) > max {
		shown = shown[len(shown)-max:]
	}
	for _, e := range shown {
		line := e
		if len(line) > 56 {
			line = line[:53] + "..."
		}
		fmt.Fprintf(w, "|   %-56s |\n", line)
	}
}

// truncString cuts s at n runes with a trailing "…" marker. Uses
// byte length instead of rune length for simplicity — service
// names in practice are ASCII, so bytes == runes. If we ever ship
// UTF-8 service names we'll switch to utf8.RuneCountInString.
func truncString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n < 3 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// debugCharToAction maps the operator's single-char input to a
// DebugAction. Letters + Ctrl-B (0x02 → shell) + Ctrl-D (0x04 →
// continue), matching the load-fail menu's shortcut convention.
// [f] is force-fail, unique to the debugger.
func debugCharToAction(b byte) DebugAction {
	switch b {
	case 'c', 'C', 0x04: // 0x04 = Ctrl-D
		return DebugContinue
	case 's', 'S', 0x02: // 0x02 = Ctrl-B
		return debugActionShell
	case 'r', 'R':
		return DebugReboot
	case 'p', 'P':
		return DebugPoweroff
	case 'f', 'F':
		return DebugForceFail
	default:
		// Unknown key → treat as continue (safest — the operator
		// hit something unintentionally, don't reboot).
		return DebugContinue
	}
}
