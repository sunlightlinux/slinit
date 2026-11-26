package main

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Step is one entry of a --retry escalation schedule for --stop:
// send Signal, wait up to Timeout for the process to exit, then move
// to the next step. A zero Signal is a wait-only step, a zero Timeout
// waits forever and noWait does not wait at all. A Repeat step sends
// nothing and continues the schedule at RepeatFrom: it is what
// "forever" becomes.
type Step struct {
	Signal     syscall.Signal
	Timeout    time.Duration
	Repeat     bool
	RepeatFrom int
}

// noWait as a Step's Timeout sends the signal and moves straight on.
const noWait time.Duration = -1

// finalSignalGrace is how long a trailing signal with no timeout after
// it gets before the schedule counts as failed.
const finalSignalGrace = time.Second

// ParseRetry parses OpenRC's / Debian's --retry schedule. A bare
// integer N is shorthand for "SIGTERM/N/SIGKILL/N"; otherwise the
// slash-separated items are signals (TERM, SIGTERM, 15, or any of
// those with one leading '-', the form start-stop-daemon(8)
// documents), timeouts in seconds, and at most one "forever", which
// repeats the items after it until the process is gone. A signal
// followed directly by another signal is sent without waiting. A
// trailing signal is sent and the process given finalSignalGrace, after
// which the schedule has failed, as OpenRC and Debian end it (and as
// slinit-start-stop-daemon does); a timeout of 0 checks once. Only a
// trailing "forever" waits with no deadline.
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
			pending.Timeout = noWait
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
		if p == "forever" {
			if repeatAt >= 0 {
				return nil, fmt.Errorf("\"forever\" more than once in %q", spec)
			}
			flush()
			repeatAt = len(steps)
			continue
		}
		if p[0] != '-' {
			if n, err := strconv.Atoi(p); err == nil {
				if !havePending {
					return nil, fmt.Errorf("timeout %d without preceding signal in %q", n, spec)
				}
				pending.Timeout = time.Duration(n) * time.Second
				if n == 0 {
					pending.Timeout = noWait
				}
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
	switch {
	case havePending:
		// Trailing signal with no timeout: send it, allow a short
		// grace for the exit to be seen, then the schedule is over.
		// Waiting forever here hung --stop (and shutdown) on a daemon
		// that ignores the signal.
		pending.Timeout = finalSignalGrace
		steps = append(steps, pending)
	case repeatAt == len(steps):
		// Trailing "forever": nothing to repeat, keep waiting.
		steps = append(steps, Step{Timeout: 0})
	case repeatAt >= 0:
		steps = append(steps, Step{Repeat: true, RepeatFrom: repeatAt})
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("no steps parsed from %q", spec)
	}
	return steps, nil
}

// runSchedule runs steps against pid and reports whether it exited.
func runSchedule(pid int, steps []Step) bool {
	for i := 0; i < len(steps); i++ {
		step := steps[i]
		if step.Repeat {
			i = step.RepeatFrom - 1
			continue
		}
		if step.Signal != 0 {
			_ = syscall.Kill(pid, step.Signal)
		}
		switch {
		case step.Timeout == noWait:
			continue
		case step.Timeout == 0:
			for processAlive(pid) {
				time.Sleep(50 * time.Millisecond)
			}
			return true
		case waitExit(pid, step.Timeout):
			return true
		}
	}
	return !processAlive(pid)
}

// scheduleBudget is how long runSchedule can take, or unbounded when
// it can wait forever (a zero timeout or a "forever").
func scheduleBudget(steps []Step) (total time.Duration, unbounded bool) {
	for _, s := range steps {
		if s.Repeat || s.Timeout == 0 {
			return 0, true
		}
		if s.Timeout > 0 {
			total += s.Timeout
		}
	}
	return total, false
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

// ParseSignal accepts "TERM", "SIGTERM", or a numeric literal.
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
