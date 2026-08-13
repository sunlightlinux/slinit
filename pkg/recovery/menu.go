// Package recovery implements slinit's interactive boot-failure
// rescue menu. When PID 1 cannot bring the system up (missing
// boot service, unresolvable dependency, config typo) the fatal
// path used to sleep 10 seconds then reboot in a loop — a
// reboot-loop trap that hides the actual error from the operator
// and gives no path to fix it without an install media.
//
// This package replaces the sleep-then-reboot with a countdown
// menu on /dev/console that offers reboot, poweroff, drop-to-shell,
// and retry-boot actions. Auto-reboots after the configured
// timeout as a safety net for headless / unattended systems so a
// bug in this code doesn't create a different flavor of stuck.
//
// Trigger site: cmd/slinit/main.go's "No boot services could be
// loaded" path (isPID1 + !containerMode). See Present().
package recovery

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Action is what the menu returns to the caller so the boot code
// can decide what to do next.
type Action int

const (
	// ActionReboot: hard reboot via the shutdown executor.
	ActionReboot Action = iota
	// ActionPoweroff: hard poweroff via the shutdown executor.
	ActionPoweroff
	// ActionRetry: caller should retry the failed boot step
	// (typically re-scan service dirs and try LoadService again).
	// Used when the operator dropped to shell, fixed a typo, and
	// wants slinit to try again without a real reboot.
	ActionRetry
	// ActionTimeout: no input within the configured window; caller
	// should treat this identically to ActionReboot (auto-reboot
	// safety net for headless systems).
	ActionTimeout
)

// String makes Action self-describing in log lines.
func (a Action) String() string {
	switch a {
	case ActionReboot:
		return "reboot"
	case ActionPoweroff:
		return "poweroff"
	case ActionRetry:
		return "retry"
	case ActionTimeout:
		return "timeout(reboot)"
	default:
		return fmt.Sprintf("Action(%d)", int(a))
	}
}

// Options tunes menu behaviour. Zero-value gives sensible defaults
// (60s timeout, /dev/console, /bin/sh + /sbin/sulogin candidates).
type Options struct {
	// Timeout is the max time to wait for operator input before
	// auto-rebooting. Zero picks DefaultTimeout.
	Timeout time.Duration
	// ConsolePath is the tty to read/write. Empty selects
	// /dev/console (correct for PID 1 on system-mode boot).
	ConsolePath string
	// ShellCandidates is the ordered list of shell binaries to try
	// when the operator picks the shell action. First one that
	// os.Stat succeeds gets executed. Empty selects the default
	// list.
	ShellCandidates []string
	// Errors is the list of ERROR lines already logged by the
	// caller — the menu re-displays them so the operator sees the
	// diagnosis without scrolling up (getty may have overwritten
	// them).
	Errors []string

	// tty is the console the caller opened — set by Present() and
	// carried through so runShell can pause raw mode around the
	// exec (shells want canonical) and restore it afterwards.
	// runCanonical (defined in tty_linux.go) captures the current
	// termios via closure.
	tty *os.File
	// runCanonical, when non-nil, temporarily restores the
	// original (canonical) termios, runs fn, then re-sets raw
	// mode. Injected by Present() so tests and the pure `present`
	// path don't drag in Linux termios syscalls.
	runCanonical func(fn func())
}

// DefaultTimeout is the auto-reboot window when the operator gives
// no input. 60s balances headless-safety (won't wait forever) with
// enough time for a person to notice the console and decide.
const DefaultTimeout = 60 * time.Second

// defaultShellCandidates matches runRescueShell in cmd/slinit/main.go.
// sulogin gets first shot because it authenticates via /etc/shadow
// before granting the shell — matters on a shared machine where
// physical console access ≠ trusted user.
//
// bash sits before /bin/sh because on serial consoles busybox ash's
// line editor is hard-coded to send `ESC[6n` cursor-position queries
// at every prompt render, regardless of TERM. The host emulator's
// reply lands on the shell's stdin as a stray `[...` command. bash's
// readline honors TERM=dumb (which slinit sets in the child env)
// and skips the query entirely, so a demo/deployment that ships
// bash gets a clean shell prompt. On minimal images without bash we
// fall through to /bin/sh with the winsize/flush workaround.
var defaultShellCandidates = []string{
	"/sbin/sulogin", "/bin/sulogin",
	"/bin/bash", "/usr/bin/bash",
	"/bin/sh", "/usr/bin/sh",
}

// Present displays the rescue menu on /dev/console and returns the
// operator's chosen action (or ActionTimeout on the safety-net path).
// The `s` action forks a shell and, on shell exit, re-enters the
// menu — so a full "fix the typo and retry" flow is one menu
// interaction: press `s`, edit the file, exit shell, press `c`.
//
// The tty is put into cbreak (canonical-off, echo-off) mode so
// every keypress — plain letters, Ctrl-B, Ctrl-D — is delivered
// immediately without requiring Enter. On exit (or before forking
// a shell) the original termios is restored.
//
// Any I/O error opening the console falls back immediately to
// ActionTimeout so a truly headless system still auto-reboots
// (the design doesn't let a broken /dev/console strand PID 1).
func Present(opts Options) Action {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.ConsolePath == "" {
		opts.ConsolePath = "/dev/console"
	}
	if len(opts.ShellCandidates) == 0 {
		opts.ShellCandidates = defaultShellCandidates
	}
	tty, err := os.OpenFile(opts.ConsolePath, os.O_RDWR, 0)
	if err != nil {
		// No console → no operator → auto-reboot safety path.
		return ActionTimeout
	}
	defer tty.Close()
	// Cbreak: char-at-a-time input, no echo. `nil` return from
	// setRawMode means "not a tty" (won't happen for /dev/console
	// but the check keeps behaviour sane if someone points opts at
	// a regular file for debug).
	orig := setRawMode(tty)
	defer restoreTermios(tty, orig)
	opts.tty = tty
	opts.runCanonical = func(fn func()) {
		// Shells expect canonical mode + echo — restore original,
		// run the shell, then re-arm raw for the next menu round.
		restoreTermios(tty, orig)
		fn()
		orig = setRawMode(tty)
	}
	return present(tty, tty, opts)
}

// present is the testable core — takes explicit reader/writer so
// tests can pipe mock console I/O without touching /dev/console.
// The public Present wraps it with the real /dev/console fd.
func present(r io.Reader, w io.Writer, opts Options) Action {
	for {
		renderMenu(w, opts)
		action := readActionWithTimeout(r, w, opts.Timeout)
		switch action {
		case ActionReboot, ActionPoweroff, ActionRetry, ActionTimeout:
			return action
		}
		// A shell action landed here; run the shell, then loop
		// back to re-present the menu.
		runShell(w, opts)
	}
}

// menuBoxBar is the horizontal bar used by every rescue-menu box
// (Present, PresentCollapse, Debugger). Fixed 60-column width matches
// serial-console defaults; changing it means changing all three
// renderers plus their tests.
const menuBoxBar = "+============================================================+"

// writeBoxHeader opens a new menu box: leading newline, bar, then the
// title line padded to the interior width. Callers follow with their
// per-menu content (writeBoxBlank / writeBoxLine / any custom Fprintf)
// and close via writeBoxFooter.
func writeBoxHeader(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", menuBoxBar)
	fmt.Fprintf(w, "| %-58s |\n", title)
}

// writeBoxBlank writes a spacer row inside the box — a `|` at each
// margin, whitespace between. Used to visually separate sections
// (title / errors / actions / footer) without cluttering the box.
func writeBoxBlank(w io.Writer) {
	fmt.Fprintf(w, "|                                                            |\n")
}

// writeBoxLine writes a single content row: `| <content> |`. content
// is formatted from format+args and truncated to fit the 58-char
// interior with "..." tail on overflow so a runaway string can't
// break the box outline on an 80-col serial console.
func writeBoxLine(w io.Writer, format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)
	if len(line) > 58 {
		line = line[:55] + "..."
	}
	fmt.Fprintf(w, "| %-58s |\n", line)
}

// writeBoxFooter writes the auto-* countdown row + closing bar +
// operator prompt. verb is "reboot" for rescue/collapse or "continue"
// for the debugger — chosen to match what the timeout actually does.
func writeBoxFooter(w io.Writer, verb string, timeout time.Duration) {
	writeBoxLine(w, "Auto-%s in %2ds if no input.", verb, int(timeout.Seconds()))
	fmt.Fprintf(w, "%s\n> ", menuBoxBar)
}

// renderMenu writes the boxed load-failure menu on w.
func renderMenu(w io.Writer, opts Options) {
	writeBoxHeader(w, "slinit: BOOT FAILURE — cannot continue")
	if len(opts.Errors) > 0 {
		writeBoxBlank(w)
		writeBoxLine(w, "Errors:")
		for _, e := range opts.Errors {
			writeBoxLine(w, "  %s", e)
		}
	}
	writeBoxBlank(w)
	writeBoxLine(w, "Choose an action:")
	writeBoxLine(w, "  [r]  reboot now")
	writeBoxLine(w, "  [p]  power off")
	writeBoxLine(w, "  [s]  drop to shell   (Ctrl-B alias)")
	writeBoxLine(w, "  [c]  continue — retry loading boot   (Ctrl-D alias)")
	writeBoxBlank(w)
	writeBoxFooter(w, "reboot", opts.Timeout)
}

// readActionWithTimeout is the load-failure-menu-flavored wrapper
// around readByteWithTimeout: reads a single key and maps it via
// charToAction. Kept as a thin shim so the shared byte-reading
// machinery lives in one place (readByteWithTimeout) and can be
// reused by PresentCollapse without duplicating the goroutine +
// countdown-tick logic.
func readActionWithTimeout(r io.Reader, w io.Writer, timeout time.Duration) Action {
	b, ok := readByteWithTimeout(r, w, timeout, "reboot")
	if !ok {
		return ActionTimeout
	}
	return charToAction(b)
}

// readByteWithTimeout reads a single non-whitespace byte from r or
// returns (0, false) if nothing arrives before timeout. Blocks up
// to timeout. Every second it re-writes the countdown line on w so
// the operator sees the clock tick — makes the UI feel alive on a
// serial console vs frozen. `verb` is the action the countdown
// describes when it expires ("reboot" for load-fail / collapse
// menus, "continue" for the debugger); callers pass the same word
// their footer used so the messages don't disagree.
//
// A read error other than io.EOF is treated as timeout (safer than
// guessing). io.EOF gets a synthetic 0x04 (Ctrl-D) so callers using
// the load-failure "Ctrl-D = continue" convention still work when
// the underlying tty is in canonical mode; callers that don't care
// about that semantic simply ignore the byte value on the caller
// side (PresentCollapse maps 0x04 to CollapseRestartBoot which is
// its analog of "continue trying to boot").
//
// Uses a goroutine + channel because os.File.Read has no native
// deadline on non-socket fds (/dev/console is a tty character
// device, not a socket).
func readByteWithTimeout(r io.Reader, w io.Writer, timeout time.Duration, verb string) (byte, bool) {
	inputCh := make(chan byte, 1)
	errCh := make(chan error, 1)
	go func() {
		br := bufio.NewReader(r)
		for {
			b, err := br.ReadByte()
			if err != nil {
				if errors.Is(err, io.EOF) {
					inputCh <- 0x04
					return
				}
				errCh <- err
				return
			}
			// Skip whitespace (line noise, trailing \n from a
			// previous line-buffered input, etc). Operator can
			// type either `r` or `r<Enter>` — both work.
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				continue
			}
			inputCh <- b
			return
		}
	}()

	// clearPrompt wipes the countdown text on the way out so any
	// [OK] name line that fires next (via ResumeBootConsole or the
	// caller's own dispatch log) starts on a clean row instead of
	// stamping over "Auto-* in Xs if no input…" leftovers.
	// \r + ANSI erase-to-end-of-line + \n moves cursor to the next
	// physical line for the follow-up output. Cheap enough to always
	// run.
	clearPrompt := func() {
		fmt.Fprint(w, "\r\x1b[2K\n")
	}

	// Simpler than Ticker: one time.After per iteration, capped at
	// the smaller of 1s (redraw cadence) and remaining time (never
	// overshoot the deadline). Earlier Ticker-based version fired
	// once then stopped delivering subsequent ticks on the demo
	// serial console — never root-caused but time.After per loop
	// dodges whatever runtime interaction bit it.
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			clearPrompt()
			return 0, false
		}
		wait := time.Second
		if remaining < wait {
			wait = remaining
		}
		select {
		case b := <-inputCh:
			clearPrompt()
			return b, true
		case <-errCh:
			clearPrompt()
			return 0, false
		case <-time.After(wait):
			secs := int(time.Until(deadline).Seconds())
			if secs <= 0 {
				clearPrompt()
				return 0, false
			}
			// \r returns cursor to start of the prompt line (which
			// is what we're on after renderXxxMenu ended with
			// "%s\n> ", bar). The %-64s pad clears any leftover
			// from the previous tick line or a stray [OK] that snuck
			// through before PauseBootConsole caught up. Trailing
			// "\r> " puts the prompt back so operator input still
			// lands right after ">".
			fmt.Fprintf(w, "\r%-64s\r> Auto-%s in %2ds if no input… (press any key)\r> ",
				"", verb, secs)
		}
	}
}

// charToAction maps the operator's single-char input to an Action.
// Both literal letters (r/p/s/c) and Ctrl-shortcuts (Ctrl-B for
// shell, Ctrl-D for continue) are recognized — the shortcuts were
// requested for muscle-memory alignment with existing debuggers /
// bootloader menus.
func charToAction(b byte) Action {
	switch b {
	case 'r', 'R':
		return ActionReboot
	case 'p', 'P':
		return ActionPoweroff
	case 's', 'S', 0x02: // 0x02 = Ctrl-B
		return actionShell // internal sentinel, not exported
	case 'c', 'C', 0x04: // 0x04 = Ctrl-D
		return ActionRetry
	default:
		// Unknown key → treat as timeout (safest — don't guess).
		// The menu will re-print if it re-loops from a shell action,
		// but from an unknown-key case we fall straight to reboot.
		return ActionTimeout
	}
}

// actionShell is a sentinel Action returned by charToAction to
// signal "fork the shell" — the public Action enum stops at
// ActionRetry because callers don't need to distinguish shell
// (the recovery package handles it internally). Value chosen
// outside the exported range so consumers never see it.
const actionShell Action = -1

// withTermDumb returns env with TERM=dumb + LINES=24 + COLUMNS=80,
// dropping any existing TERM/LINES/COLUMNS the parent inherited so
// our values are the ones the child sees. Exposed as a package-level
// helper so cmd/slinit's rescue + debug-shell paths can use the same
// canonical env-shape.
func withTermDumb(env []string) []string {
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "TERM="):
		case strings.HasPrefix(kv, "LINES="):
		case strings.HasPrefix(kv, "COLUMNS="):
		default:
			out = append(out, kv)
		}
	}
	return append(out, "TERM=dumb", "LINES=24", "COLUMNS=80")
}

// WithTermDumbEnv is the exported form for callers outside this
// package (cmd/slinit uses it for the rescue + debug-shell paths).
func WithTermDumbEnv(env []string) []string { return withTermDumb(env) }

// runShell execs the first available shell candidate on /dev/console.
// Mirrors cmd/slinit/main.go's runRescueShell but takes the console
// writer from the outer scope so any error messages appear in-line
// with the menu. Thin wrapper over forkShellOnConsole; kept as a
// separate function so callers using Options don't have to unpack
// its fields at the call site.
func runShell(w io.Writer, opts Options) {
	forkShellOnConsole(w, opts.ShellCandidates, opts.ConsolePath, opts.runCanonical)
}

// forkShellOnConsole picks the first present shell from candidates,
// re-opens ConsolePath as the child's stdin/stdout/stderr (a fresh
// fd — reusing the parent's raw-mode fd would confuse the child's
// line editing), and runs it with Setsid+Setctty so it becomes its
// own session leader with the console as its controlling tty.
//
// When runCanonical is non-nil, the shell is fork'd inside it —
// canonical mode restored around the exec, raw mode re-armed on
// return — so the shell gets normal line editing / echo but the
// caller's next read is back in cbreak. When runCanonical is nil
// (tests, non-tty callers) the shell runs directly.
//
// The tty is temporarily restored to canonical mode via opts.runCanonical
// (set by Present) so the shell inherits normal line editing / echo,
// then re-armed to cbreak on return so the next menu iteration
// keeps its single-keypress input.
func forkShellOnConsole(w io.Writer, candidates []string, consolePath string, runCanonical func(fn func())) {
	var shell string
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			shell = p
			break
		}
	}
	if shell == "" {
		fmt.Fprintf(w, "\n[recovery] no shell found in %v; returning to menu\n", candidates)
		return
	}
	runIt := func() {
		fmt.Fprintf(w, "\n[recovery] exec %s (exit to return to menu)\n", shell)
		cmd := exec.Command(shell)
		if tty, err := os.OpenFile(consolePath, os.O_RDWR, 0); err == nil {
			// Seed the winsize BEFORE we drain: shells that fall back
			// to `ESC[6n` cursor-pos queries when winsize is 0x0 will
			// stop querying once TIOCGWINSZ returns real numbers.
			// Without this, drain-then-exec still lets a fresh burst
			// of replies land on the shell's stdin as soon as its
			// line editor renders the first prompt.
			seedWinsize(tty)
			// Drain stray terminal replies (cursor-position reports,
			// device-attributes responses) that the emulator may have
			// queued while the menu was drawing ANSI escapes. Without
			// this, the shell's first read returns bytes like "[5R"
			// or "[40;5R" and treats them as commands. See flushInput
			// for the full context.
			flushInput(tty)
			cmd.Stdin = tty
			cmd.Stdout = tty
			cmd.Stderr = tty
			defer tty.Close()
		}
		// Force TERM=dumb + explicit LINES/COLUMNS on serial recovery.
		// TERM=dumb is the well-known signal to shells and readline-
		// like libraries to disable ANSI escape emission entirely
		// (no color, no cursor queries, no fancy line editing).
		// Without it, busybox ash's libbb/lineedit sends `ESC[6n`
		// cursor-position queries on every prompt render and the
		// host terminal's replies land back on the shell's stdin as
		// stray `[38;5R` "commands". Winsize alone doesn't fix this
		// — the queries fire regardless of TIOCGWINSZ. Filter out
		// the caller's TERM instead of appending so ours wins.
		cmd.Env = withTermDumb(os.Environ())
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(w, "\n[recovery] shell exited with error: %v\n", err)
		}
	}
	if runCanonical != nil {
		runCanonical(runIt)
	} else {
		// Tests / non-tty callers: no termios juggling needed.
		runIt()
	}
}
