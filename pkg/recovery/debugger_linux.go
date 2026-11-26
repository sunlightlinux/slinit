package recovery

import (
	"encoding/binary"
	"errors"
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
	// PauseBootConsoleFn / ResumeBootConsoleFn, when non-nil, are
	// invoked around each menu render so concurrent "[ OK ] name"
	// lines from services finishing while the operator reads the
	// menu don't trample the boxed layout. Wired by main.go to
	// logger.PauseBootConsole / ResumeBootConsole. Nil-safe: on
	// non-PID-1 mode or tests, the menu still renders correctly,
	// just with the old interleaving behaviour.
	PauseBootConsoleFn  func()
	ResumeBootConsoleFn func()
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
	// wakeFd is an eventfd in the reader's poll set. Stop writes to it
	// so the reader wakes at once; closing the tty does not wake a
	// poll(2) already parked on it.
	wakeFd int
	// menuIn is what the menu reads keys from: the tty, through a read
	// a timed-out menu can cancel.
	menuIn *pollReader
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
	wakeFd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		tty.Close()
		d.running.Store(false)
		close(d.doneCh)
		return fmt.Errorf("eventfd: %w", err)
	}
	cancelFd, err := unix.Eventfd(0, unix.EFD_CLOEXEC|unix.EFD_NONBLOCK)
	if err != nil {
		unix.Close(wakeFd)
		tty.Close()
		d.running.Store(false)
		close(d.doneCh)
		return fmt.Errorf("eventfd: %w", err)
	}
	d.tty = tty
	d.wakeFd = wakeFd
	orig := setRawMode(tty)
	d.termiosMu.Lock()
	d.termios = orig
	d.termiosMu.Unlock()
	// Read the fd here, not inside the goroutine. Stop() closes d.tty to
	// unstick the blocking read, and os.File.Fd() touches the same
	// internal state Close() writes without taking the file's mutex — so
	// a Stop() landing in the window before the goroutine got that far
	// was a data race, and a lost race means polling a descriptor number
	// that has since been reused by something else.
	fd := int(tty.Fd())
	d.menuIn = &pollReader{fd: fd, cancelFd: cancelFd}
	go d.run(fd, wakeFd)
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
	// If a menu is presenting right now (operator hit Ctrl-B and is
	// reading / interacting) do NOT rip the tty out from under them.
	// Wait for the menu to close on its own (operator picks an
	// action or 60s auto-continue timeout). This is the difference
	// between "boot completed while you were browsing the debugger
	// and we killed your session" and "boot completed silently in
	// the background, come back to us when you're done".
	//
	// menuMu is held only during presentMenu, so this normally
	// returns immediately; the wait matters only when a real menu
	// is up.
	d.menuMu.Lock()
	d.finishStop()
}

// StopOrDefer is Stop for a caller that must not wait on the operator:
// ProcessService.BringUp calls it with the scheduling lock held, just
// before a console service inherits /dev/console. With no menu open it
// stops inline, so termios is back to the original before the child
// captures it. With a menu open it returns at once and the stop runs on
// its own goroutine once the menu closes: waiting inline froze every
// start, stop and control command for as long as the operator read the
// menu (each redraw allows 60s) or sat in its shell (no limit at all).
//
// The price, only when the operator has the menu open as the console
// service starts: that service starts on a console still in the menu's
// raw mode and shared with the menu's reader until the menu closes. A
// login shell may then keep echo off; Stop puts the original mode back
// afterwards only if nothing has changed it since.
func (d *Debugger) StopOrDefer() {
	if !d.running.Load() {
		return
	}
	if !d.menuMu.TryLock() {
		go d.Stop()
		return
	}
	if !d.running.CompareAndSwap(true, false) {
		d.menuMu.Unlock()
		return
	}
	d.finishStop()
}

// finishStop is the rest of Stop. Called with menuMu held and running
// already cleared; releases menuMu.
func (d *Debugger) finishStop() {
	// stopCh closes under menuMu, so a Ctrl-B that arrives from here
	// on finds it closed and opens no menu.
	close(d.stopCh)
	d.menuMu.Unlock()
	// Wake the reader through the eventfd and wait for it to leave
	// before closing anything: closing the tty does not wake a parked
	// poll(2), which left Stop waiting for the next poll tick (up to
	// 200ms, under the scheduling lock, on every boot), and a reader
	// still running after the close could poll or read a descriptor
	// number that had since been reused.
	var one [8]byte
	binary.NativeEndian.PutUint64(one[:], 1)
	_, _ = unix.Write(d.wakeFd, one[:])
	<-d.doneCh
	if d.tty != nil {
		d.tty.Close()
	}
	unix.Close(d.wakeFd)
	unix.Close(d.menuIn.cancelFd)
	// Restore termios AFTER goroutine exit. termios is per-tty
	// kernel state (not per-fd), so we re-open the console just
	// for the ioctl — the goroutine's fd is closed and calling
	// TCSETS on it would silently return EBADF, leaving the tty
	// stuck in raw mode. When bash / getty then opens the console,
	// it inherits ICANON off + ECHO off and typed characters
	// vanish (this exact bug shipped in the initial cut of
	// debugger phase 1).
	d.termiosMu.Lock()
	orig := d.termios
	d.termios = nil
	d.termiosMu.Unlock()
	if orig != nil {
		if f, err := os.OpenFile(d.opts.ConsolePath, os.O_RDWR, 0); err == nil {
			restoreIfStillRaw(f, orig)
			f.Close()
		}
	}
	if d.opts.Logger != nil {
		d.opts.Logger.Info("Boot debugger stopped")
	}
}

// run is the reader loop. Byte-at-a-time in raw mode, filters for
// Ctrl-B (0x02), opens the menu on hit. Every other byte is
// silently dropped — a stray keypress at boot time shouldn't
// trigger anything, and letting bytes accumulate on getty would
// break the eventual login flow (mitigated by Stop before getty).
//
// Poll instead of a naked ReadByte because Linux does not unblock a
// pending tty read when the fd is Close()'d from another goroutine —
// the driver keeps the reader parked in the wait queue independently
// of which fd initiated the read. Stop wakes the poll through wakeFd
// instead, and closes the tty only once this loop has returned.
// fd is passed in rather than read from d.tty: see the comment at the
// `go d.run(...)` call site.
func (d *Debugger) run(fd, wakeFd int) {
	defer close(d.doneCh)
	buf := make([]byte, 1)
	pollFds := []unix.PollFd{
		{Fd: int32(fd), Events: unix.POLLIN},
		{Fd: int32(wakeFd), Events: unix.POLLIN},
	}
	for {
		select {
		case <-d.stopCh:
			return
		default:
		}
		pollFds[0].Revents = 0
		pollFds[1].Revents = 0
		_, err := unix.Poll(pollFds, -1)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if pollFds[1].Revents != 0 {
			return // Stop
		}
		if pollFds[0].Revents&(unix.POLLHUP|unix.POLLNVAL|unix.POLLERR) != 0 {
			return // console hung up or errored
		}
		if pollFds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		nr, err := unix.Read(fd, buf)
		if err != nil {
			if err == unix.EINTR || err == unix.EAGAIN {
				continue
			}
			return
		}
		if nr == 0 {
			continue
		}
		if buf[0] != 0x02 { // only Ctrl-B triggers the menu
			continue
		}
		// Serialize with itself — the menu presentation is not
		// reentrant. A second Ctrl-B while the menu is open would
		// interleave input, so we hold menuMu across the whole
		// present-and-dispatch cycle.
		d.menuMu.Lock()
		select {
		case <-d.stopCh:
			d.menuMu.Unlock()
			return
		default:
		}
		action := d.presentMenu()
		d.menuMu.Unlock()
		d.dispatch(action)
	}
}

// presentMenu is the outer menu loop: render + read + handle-shell.
// Returns when the operator picks a non-shell action (or menu times
// out). Runs on the reader goroutine; the tty is already in raw
// mode from Start.
//
// Pauses the boot console renderer for the whole present cycle so
// concurrent "[ OK ] name" lines from services that finish while
// the operator reads the menu don't shatter the boxed layout.
// Resume on exit — the snapshot inside the box already reflects
// live state per render, so the operator loses nothing by not
// seeing the individual completion lines.
func (d *Debugger) presentMenu() DebugAction {
	if d.opts.PauseBootConsoleFn != nil {
		d.opts.PauseBootConsoleFn()
	}
	if d.opts.ResumeBootConsoleFn != nil {
		defer d.opts.ResumeBootConsoleFn()
	}
	// Pausing the boot console only stops lines not yet started. Every
	// service transition prints from its own goroutine, so one already
	// past the paused check is still on its way to the console, and a
	// status line on a serial console takes milliseconds to drain.
	//
	// A settle gives those writes somewhere to go before the frame is
	// drawn, and the clear-screen in box.header then wipes whatever
	// landed.
	//
	// UNVERIFIED against the symptom that prompted it: a demo boot
	// showed five "[ OK ] name" lines sitting between the top bar and
	// the title, with the box cut open around them. That could not be
	// reproduced here — tests/functional/debugger-menu-test.sh drives
	// Ctrl-B into a boot with forty services completing at the same
	// instant and the box comes out clean three times out of three even
	// with this settle and the clear-screen removed. So treat this as a
	// cheap defence whose effect on that report is unproven, and do not
	// record the report as fixed.
	//
	// It is a pause and not a lock on purpose: bootStatus is called from
	// the state machine's goroutines, so a console mutex held across an
	// interactive menu would stall boot progress for as long as the
	// operator reads the screen — up to the whole timeout.
	time.Sleep(consoleSettle)

	// One snapshot and one box per pass: the state machine keeps
	// running while the menu is open, the console may have been
	// resized, and a shell the operator dropped into could have
	// changed the tty mode.
	for {
		snap := d.opts.StatusFn()
		snap.Elapsed = time.Since(d.startTime)
		renderedAt := time.Now()
		bx := newBox(d.tty)
		renderDebugMenu(bx, snap, d.opts.Timeout)
		c, ok := readByteWithTimeout(d.menuIn, bx, d.opts.Timeout, "continue")
		if !ok {
			return DebugTimeout
		}

		switch action := debugCharToAction(c); action {
		case debugActionShell:
			// Temporarily restore canonical mode, fork the shell,
			// re-arm raw mode on return, then loop back so the
			// operator gets a fresh look at status.
			d.runShell()
		case DebugForceFail:
			// Acted on here rather than returned, so the loop can
			// redraw afterwards. The list on screen is a still photo
			// of a system that is still moving: without a redraw the
			// operator is left reading seven in-progress services
			// beside a message saying there are none.
			d.forceFailFirst(time.Since(renderedAt))
		default:
			return action
		}
	}
}

// consoleSettle is how long to let in-flight boot-console writes land
// before drawing over them. Long enough for a print already past the
// paused check, short enough to be invisible to the operator.
const consoleSettle = 50 * time.Millisecond

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
		// Reached only if something dispatches this out of band;
		// presentMenu handles it in-loop so it can redraw after.
		d.forceFailFirst(0)
	}
}

// forceFailFirst force-fails the first service the last snapshot showed
// as in progress. age is how old that snapshot was, and it is reported
// rather than swallowed: the state machine keeps running while the menu
// is open, so "nothing in progress" is a perfectly normal answer to a
// list that was true a moment ago — and saying only "nothing to
// force-fail" under a list of seven reads like a bug in slinit.
func (d *Debugger) forceFailFirst(age time.Duration) {
	log := d.opts.Logger
	snap := d.opts.StatusFn()
	if len(snap.InProgress) == 0 {
		if log != nil {
			log.Warn("Debug menu: force-fail requested but no service is in progress now "+
				"(the list shown was %s old)", age.Round(time.Millisecond))
		}
		fmt.Fprintf(d.tty, "\n[debug] nothing in progress now — everything listed "+
			"above finished while the menu was open (%s ago)\n",
			age.Round(time.Millisecond))
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

// renderDebugMenu writes the boxed debug menu on w. Uses the same
// visual language as renderMenu (load-fail) and renderCollapseMenu
// so the three boot-failure prompts feel like siblings. Truncates
// service names + notes so the box stays visually intact on
// 80-col serial consoles.
func renderDebugMenu(b *box, snap StatusSnapshot, timeout time.Duration) {
	b.header(fmt.Sprintf("slinit: BOOT DEBUGGER — Ctrl-B intercepted at %6.2fs", snap.Elapsed.Seconds()))
	renderServiceBlock(b, "In progress", snap.InProgress, 5)
	renderServiceBlock(b, "Waiting on deps", snap.Waiting, 5)
	renderErrorBlock(b, snap.RecentErrors)
	b.blank()
	b.line("Actions:")
	b.action("  [c] / Ctrl-D   continue boot")
	b.action("  [s] / Ctrl-B   drop to shell")
	b.action("  [f]            force-fail first in-progress service")
	b.action("  [r]            reboot           [p]  power off")
	b.blank()
	b.footer("continue", timeout)
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
func renderServiceBlock(b *box, title string, svcs []ServiceInfo, max int) {
	if len(svcs) == 0 {
		return
	}
	b.blank()
	b.line("%s (%d):", title, len(svcs))
	shown := svcs
	if len(shown) > max {
		shown = shown[:max]
	}
	for _, s := range shown {
		// Pad the name by display columns rather than with %-20s, which
		// counts bytes and so misaligns the State column for any name
		// that is not pure ASCII.
		name := padTo(truncString(s.Name, 20), 20)
		if s.Note != "" {
			b.line("  %s %s (%s)", name, s.State, s.Note)
		} else {
			b.line("  %s %s", name, s.State)
		}
	}
	if len(svcs) > max {
		b.line("  ... +%d more", len(svcs)-max)
	}
}

// renderErrorBlock prints the "Recent errors" section, mirroring
// renderServiceBlock's shape. Cap at 3 lines to keep the menu
// tight; full history is in the log.
func renderErrorBlock(b *box, errs []string) {
	if len(errs) == 0 {
		return
	}
	b.blank()
	b.line("Recent errors (last %d):", len(errs))
	shown := errs
	const max = 3
	if len(shown) > max {
		shown = shown[len(shown)-max:]
	}
	for _, e := range shown {
		b.bad("  %s", e)
	}
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

// pollReader reads the console one byte at a time, and its read can be
// cancelled. readByte reads on a goroutine of its own, and one left
// blocked on a plain tty read after a menu timed out took the
// operator's next key and dropped it: the next Ctrl-B, or once the
// debugger had stopped, the first key typed at the login prompt.
type pollReader struct {
	fd       int
	cancelFd int // eventfd; readable means "give up"
}

var errReadCancelled = errors.New("read cancelled")

// Read returns at most one byte, so nothing typed after the key the menu
// wanted is left in a buffer that dies with the reading goroutine.
func (r *pollReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	fds := []unix.PollFd{
		{Fd: int32(r.fd), Events: unix.POLLIN},
		{Fd: int32(r.cancelFd), Events: unix.POLLIN},
	}
	for {
		fds[0].Revents, fds[1].Revents = 0, 0
		if _, err := unix.Poll(fds, -1); err != nil {
			if err == unix.EINTR {
				continue
			}
			return 0, err
		}
		if fds[1].Revents != 0 {
			return 0, errReadCancelled
		}
		n, err := unix.Read(r.fd, p[:1])
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}

// cancelRead stops a Read in progress, waits for the goroutine reading
// to exit, and then re-arms the reader for the next menu.
func (r *pollReader) cancelRead(exited <-chan struct{}) {
	var one [8]byte
	binary.NativeEndian.PutUint64(one[:], 1)
	_, _ = unix.Write(r.cancelFd, one[:])
	<-exited
	_, _ = unix.Read(r.cancelFd, one[:])
}
