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
// to the next step. Copied verbatim from slinit-start-stop-daemon so
// the two tools accept identical --retry syntax.
type Step struct {
	Signal  syscall.Signal
	Timeout time.Duration
}

// ParseRetry mirrors slinit-start-stop-daemon's parser: bare integer
// = shorthand for "SIGTERM/N/SIGKILL/N", otherwise slash-separated
// "SIG/timeout/SIG/timeout…". A trailing signal with no timeout means
// "wait forever".
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
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty token in retry spec %q", spec)
		}
		if n, err := strconv.Atoi(p); err == nil {
			if !havePending {
				return nil, fmt.Errorf("timeout %d without preceding signal in %q", n, spec)
			}
			if n < 0 {
				return nil, fmt.Errorf("negative timeout in %q", spec)
			}
			pending.Timeout = time.Duration(n) * time.Second
			steps = append(steps, pending)
			pending = Step{}
			havePending = false
			continue
		}
		sig, err := ParseSignal(p)
		if err != nil {
			return nil, fmt.Errorf("bad token %q in retry spec: %w", p, err)
		}
		if havePending {
			steps = append(steps, pending)
		}
		pending = Step{Signal: sig}
		havePending = true
	}
	if havePending {
		pending.Timeout = 0
		steps = append(steps, pending)
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
