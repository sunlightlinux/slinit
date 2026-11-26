package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Step is one entry in a stop-retry schedule: send Signal, then wait
// up to Timeout for the process to exit. A zero-value signal marks a
// "wait-only" step (the tail of "TERM/30/forever"). A Step with Repeat
// set is the "forever" marker: when the schedule runs out, it starts
// again from the step after the marker.
type Step struct {
	Signal  syscall.Signal
	Timeout time.Duration
	Repeat  bool
}

const (
	// noWait as a Step's Timeout sends the signal and reports success
	// without waiting for the process to exit (--stop --oknodo).
	noWait time.Duration = -1
	// sendOnly sends the signal and moves straight on to the next
	// step, as a signal item with no timeout after it does in Debian
	// and OpenRC. As the last step it ends the schedule: the process
	// is given sendOnlyGrace to be gone, then reported as refusing to
	// die.
	sendOnly time.Duration = -2
	// waitForever waits for the process to exit with no deadline.
	waitForever time.Duration = math.MaxInt64
)

// sendOnlyGrace is how long a final signal with no timeout after it
// is given to take effect (a SIGKILLed process still has to be reaped
// by its parent before kill(pid, 0) stops finding it).
var sendOnlyGrace = time.Second

// ParseRetry accepts either an integer (seconds, treated as the
// classic "SIGTERM/N/SIGKILL/N" schedule) or a slash-separated
// schedule as Debian and OpenRC document it: "TERM/30/KILL/5".
//
//   - A signal is a name with or without the SIG prefix, or a number,
//     optionally written with one leading '-' (-TERM, -9). A plain
//     number is a timeout in seconds, so the signal 9 is written -9.
//   - "forever" repeats the items after it indefinitely once the
//     schedule runs out (TERM/5/forever/KILL/5 sends KILL every 5 s).
//     Debian refuses "forever" as the last item; OpenRC repeats the
//     item before it, so TERM/30/forever waits for the process with no
//     deadline. We follow OpenRC there; it needs a timeout before it.
//   - A signal with no timeout after it is sent and the schedule goes
//     on; if it is the last item the process gets sendOnlyGrace and is
//     then reported as refusing to die, as Debian ends a schedule.
//     It used to mean "wait forever", so TERM/5/KILL hung silently on
//     a process KILL could not remove (D state, ptrace stop).
//
// Ambiguous case: a bare integer means "SIGTERM/N/SIGKILL/N" per
// Debian start-stop-daemon(8).
func ParseRetry(spec string, defaultSig syscall.Signal) ([]Step, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty retry spec")
	}
	if n, err := strconv.Atoi(spec); err == nil {
		if n <= 0 {
			return nil, fmt.Errorf("retry timeout must be positive")
		}
		return []Step{
			{Signal: defaultSig, Timeout: time.Duration(n) * time.Second},
			{Signal: syscall.SIGKILL, Timeout: time.Duration(n) * time.Second},
		}, nil
	}

	parts := strings.Split(spec, "/")
	var steps []Step
	var pending Step
	var havePending bool
	repeatAt := -1
	flush := func() {
		if havePending {
			pending.Timeout = sendOnly
			steps = append(steps, pending)
			pending = Step{}
			havePending = false
		}
	}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty token in retry spec %q", spec)
		}
		if strings.EqualFold(p, "forever") {
			if repeatAt >= 0 {
				return nil, fmt.Errorf("invalid retry spec %q: 'forever' appears more than once", spec)
			}
			flush()
			steps = append(steps, Step{Repeat: true})
			repeatAt = len(steps)
			continue
		}
		if !strings.HasPrefix(p, "-") {
			if n, err := strconv.Atoi(p); err == nil {
				if !havePending {
					return nil, fmt.Errorf("timeout %d without preceding signal in %q", n, spec)
				}
				pending.Timeout = time.Duration(n) * time.Second
				steps = append(steps, pending)
				pending = Step{}
				havePending = false
				continue
			}
		}
		sig, err := ParseSignal(strings.TrimPrefix(p, "-"))
		if err != nil {
			return nil, fmt.Errorf("bad token %q in retry spec: %w", p, err)
		}
		flush()
		pending = Step{Signal: sig}
		havePending = true
	}
	flush()
	if repeatAt == len(steps) {
		// "forever" last: repeat the timeout before it, i.e. keep
		// waiting (OpenRC). After a bare signal there is nothing to
		// repeat but the signal, which would be a busy loop.
		if repeatAt < 2 || steps[repeatAt-2].Timeout <= 0 {
			return nil, fmt.Errorf("invalid retry spec %q: 'forever' must follow a timeout or precede the items to repeat", spec)
		}
		steps = append(steps, Step{Timeout: waitForever})
	} else if repeatAt >= 0 {
		waits := false
		for _, st := range steps[repeatAt:] {
			if st.Timeout > 0 {
				waits = true
			}
		}
		if !waits {
			return nil, fmt.Errorf("invalid retry spec %q: the items after 'forever' need a non-zero timeout", spec)
		}
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no steps parsed from %q", spec)
	}
	return steps, nil
}

var signalNames = map[string]syscall.Signal{
	"HUP":    syscall.SIGHUP,
	"INT":    syscall.SIGINT,
	"QUIT":   syscall.SIGQUIT,
	"ILL":    syscall.SIGILL,
	"TRAP":   syscall.SIGTRAP,
	"ABRT":   syscall.SIGABRT,
	"IOT":    syscall.SIGIOT,
	"BUS":    syscall.SIGBUS,
	"FPE":    syscall.SIGFPE,
	"KILL":   syscall.SIGKILL,
	"USR1":   syscall.SIGUSR1,
	"SEGV":   syscall.SIGSEGV,
	"USR2":   syscall.SIGUSR2,
	"PIPE":   syscall.SIGPIPE,
	"ALRM":   syscall.SIGALRM,
	"TERM":   syscall.SIGTERM,
	"STKFLT": syscall.SIGSTKFLT,
	"CHLD":   syscall.SIGCHLD,
	"CONT":   syscall.SIGCONT,
	"STOP":   syscall.SIGSTOP,
	"TSTP":   syscall.SIGTSTP,
	"TTIN":   syscall.SIGTTIN,
	"TTOU":   syscall.SIGTTOU,
	"URG":    syscall.SIGURG,
	"XCPU":   syscall.SIGXCPU,
	"XFSZ":   syscall.SIGXFSZ,
	"VTALRM": syscall.SIGVTALRM,
	"PROF":   syscall.SIGPROF,
	"WINCH":  syscall.SIGWINCH,
	"IO":     syscall.SIGIO,
	"PWR":    syscall.SIGPWR,
	"SYS":    syscall.SIGSYS,
}

// ParseSignal accepts "TERM", "SIGTERM", or "15".
func ParseSignal(s string) (syscall.Signal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty signal")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 || n > 64 {
			return 0, fmt.Errorf("signal number %d out of range", n)
		}
		return syscall.Signal(n), nil
	}
	upper := strings.ToUpper(s)
	upper = strings.TrimPrefix(upper, "SIG")
	if sig, ok := signalNames[upper]; ok {
		return sig, nil
	}
	return 0, fmt.Errorf("unknown signal %q", s)
}
