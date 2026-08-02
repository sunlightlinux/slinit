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
}

// DefaultTimeout is the auto-reboot window when the operator gives
// no input. 60s balances headless-safety (won't wait forever) with
// enough time for a person to notice the console and decide.
const DefaultTimeout = 60 * time.Second

// defaultShellCandidates matches runRescueShell in cmd/slinit/main.go.
// sulogin gets first shot because it authenticates via /etc/shadow
// before granting the shell — matters on a shared machine where
// physical console access ≠ trusted user.
var defaultShellCandidates = []string{
	"/sbin/sulogin", "/bin/sulogin",
	"/bin/sh", "/usr/bin/sh",
}

// Present displays the rescue menu on /dev/console and returns the
// operator's chosen action (or ActionTimeout on the safety-net path).
// The `s` action forks a shell and, on shell exit, re-enters the
// menu — so a full "fix the typo and retry" flow is one menu
// interaction: press `s`, edit the file, exit shell, press `c`.
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

// renderMenu writes the boxed menu + errors + prompt to w. Kept as
// a single Fprintf so partial writes don't leave a half-drawn box
// on a slow serial console.
func renderMenu(w io.Writer, opts Options) {
	const bar = "+============================================================+"
	var errsBlock strings.Builder
	if len(opts.Errors) > 0 {
		errsBlock.WriteString("|                                                            |\n")
		errsBlock.WriteString("| Errors:                                                    |\n")
		for _, e := range opts.Errors {
			// Truncate very long errors so the box stays visually
			// intact on 80-col consoles. Full text is still in the
			// scrollback (logger already printed it).
			line := e
			if len(line) > 56 {
				line = line[:53] + "..."
			}
			fmt.Fprintf(&errsBlock, "|   %-56s |\n", line)
		}
	}
	fmt.Fprintf(w, `
%s
| slinit: BOOT FAILURE — cannot continue                     |%s|                                                            |
| Choose an action:                                          |
|   [r]  reboot now                                          |
|   [p]  power off                                           |
|   [s]  drop to shell   (Ctrl-B alias)                      |
|   [c]  continue — retry loading boot   (Ctrl-D alias)      |
|                                                            |
| Auto-reboot in %2ds if no input.                            |
%s
> `,
		bar,
		"\n"+errsBlock.String(),
		int(opts.Timeout.Seconds()),
		bar,
	)
}

// readActionWithTimeout reads a single character (or a line — we
// accept both since serial consoles often deliver line-buffered
// input) and maps it to an Action. Blocks up to timeout; returns
// ActionTimeout if nothing arrives.
//
// Uses a goroutine + channel because os.File.Read has no native
// deadline on non-socket fds (/dev/console is a tty character
// device, not a socket).
func readActionWithTimeout(r io.Reader, w io.Writer, timeout time.Duration) Action {
	inputCh := make(chan byte, 1)
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		br := bufio.NewReader(r)
		for {
			b, err := br.ReadByte()
			if err != nil {
				// EOF on a canonical-mode tty = operator pressed
				// Ctrl-D on an empty line (the kernel doesn't
				// deliver the 0x04 byte, it delivers zero-byte
				// read → io.EOF). Map that to a synthetic 0x04
				// so charToAction routes it to ActionRetry —
				// matches the "Ctrl-D = continue" semantic the
				// menu documents. Every other error stays as
				// timeout (safer than guessing).
				if errors.Is(err, io.EOF) {
					inputCh <- 0x04
					return
				}
				errCh <- err
				return
			}
			// Skip whitespace (line noise, trailing \n from a
			// previous line-buffered input, etc). This means the
			// operator can type either `r` or `r<Enter>` — both
			// work.
			if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
				continue
			}
			buf[0] = b
			inputCh <- b
			return
		}
	}()

	// Countdown timer: refresh the "Auto-reboot in Xs" line every
	// second so the operator sees the clock tick. Not strictly
	// necessary but makes the UI feel alive vs frozen.
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	for {
		select {
		case b := <-inputCh:
			return charToAction(b)
		case <-errCh:
			return ActionTimeout
		case <-time.After(time.Until(deadline)):
			return ActionTimeout
		case <-tick.C:
			remaining := int(time.Until(deadline).Seconds())
			if remaining <= 0 {
				return ActionTimeout
			}
			// Overwrite the previous countdown line with \r.
			fmt.Fprintf(w, "\r| Auto-reboot in %2ds if no input.                            |\r> ", remaining)
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

// runShell execs the first available shell candidate on /dev/console.
// Mirrors cmd/slinit/main.go's runRescueShell but takes the console
// writer from the outer scope so any error messages appear in-line
// with the menu.
func runShell(w io.Writer, opts Options) {
	var shell string
	for _, p := range opts.ShellCandidates {
		if _, err := os.Stat(p); err == nil {
			shell = p
			break
		}
	}
	if shell == "" {
		fmt.Fprintf(w, "\n[recovery] no shell found in %v; returning to menu\n", opts.ShellCandidates)
		return
	}
	fmt.Fprintf(w, "\n[recovery] exec %s (exit to return to menu)\n", shell)
	cmd := exec.Command(shell)
	// Re-open the console for the shell — reusing the outer tty
	// fd would confuse the child's raw-mode / echo settings.
	if tty, err := os.OpenFile(opts.ConsolePath, os.O_RDWR, 0); err == nil {
		cmd.Stdin = tty
		cmd.Stdout = tty
		cmd.Stderr = tty
		defer tty.Close()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(w, "\n[recovery] shell exited with error: %v\n", err)
	}
}
