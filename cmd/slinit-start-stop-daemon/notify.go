package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// notifyProto is the parsed form of the --notify spec.
//
// Only the "readiness=" family is accepted. Supported readiness values:
//
//	none                — no wait; exec success = ready
//	manual              — application-owned; alias of none for us
//	pidfile             — poll until --pidfile appears
//	fd:N                — child writes to inherited fd N (3..9)
//	stderr              — child's first stderr line signals ready
//	signal[:SIG]        — child sends SIG to its parent (default SIGUSR1)
type notifyProto struct {
	mode   string
	fdNum  int            // fd mode
	signal syscall.Signal // signal mode
}

// parseNotify normalises spec into a notifyProto. Empty spec resolves to
// mode="none" so callers can invoke unconditionally.
func parseNotify(spec string) (notifyProto, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return notifyProto{mode: "none"}, nil
	}
	kv := strings.SplitN(spec, "=", 2)
	if len(kv) != 2 || strings.TrimSpace(kv[0]) != "readiness" {
		return notifyProto{}, fmt.Errorf("only 'readiness=' form is supported (got %q)", spec)
	}
	val := strings.TrimSpace(kv[1])
	switch {
	case val == "none":
		return notifyProto{mode: "none"}, nil
	case val == "manual":
		return notifyProto{mode: "manual"}, nil
	case val == "pidfile":
		return notifyProto{mode: "pidfile"}, nil
	case val == "stderr":
		return notifyProto{mode: "stderr"}, nil
	case strings.HasPrefix(val, "fd:"):
		n, err := strconv.Atoi(val[len("fd:"):])
		if err != nil {
			return notifyProto{}, fmt.Errorf("fd:N: %w", err)
		}
		// Refuse stdio slots (would clobber the child's std streams) and
		// anything past 9 (padding ExtraFiles with /dev/null gets silly
		// beyond that).
		if n < 3 || n > 9 {
			return notifyProto{}, fmt.Errorf("fd:N must be in [3,9] (got %d)", n)
		}
		return notifyProto{mode: "fd", fdNum: n}, nil
	case val == "signal" || strings.HasPrefix(val, "signal:"):
		sig := syscall.SIGUSR1
		if strings.HasPrefix(val, "signal:") {
			s, err := ParseSignal(val[len("signal:"):])
			if err != nil {
				return notifyProto{}, fmt.Errorf("signal spec: %w", err)
			}
			sig = s
		}
		return notifyProto{mode: "signal", signal: sig}, nil
	default:
		return notifyProto{}, fmt.Errorf("readiness=%s is not supported", val)
	}
}

// notifyState carries pre-fork resources whose parent-side halves must
// be closed after fork so EOFs are observable and we don't leak fds.
type notifyState struct {
	proto    notifyProto
	readEnd  *os.File       // parent's read side (fd + stderr modes)
	writeEnd *os.File       // parent's ref to child's write side; closed post-fork
	pads     []*os.File     // /dev/null fillers for fd:N padding; closed post-fork
	sigCh    chan os.Signal // signal mode
}

// prepareNotify wires whatever pre-fork state the notify mode needs
// into cmd. It must be called AFTER setupStdio so its stderr override
// (for readiness=stderr) wins over the default os.Stderr / DevNull.
func prepareNotify(cmd *exec.Cmd, opts Options) (*notifyState, error) {
	proto, err := parseNotify(opts.Notify)
	if err != nil {
		return nil, err
	}
	st := &notifyState{proto: proto}
	switch proto.mode {
	case "none", "manual", "pidfile":
		return st, nil
	case "fd":
		// ExtraFiles[i] becomes fd (3+i) in the child, so pad
		// with /dev/null for indices below fdNum-3 before appending
		// our real write end.
		padCount := proto.fdNum - 3
		for i := 0; i < padCount; i++ {
			d, err := os.Open(os.DevNull)
			if err != nil {
				st.closeAll()
				return nil, fmt.Errorf("readiness=fd: pad: %w", err)
			}
			st.pads = append(st.pads, d)
			cmd.ExtraFiles = append(cmd.ExtraFiles, d)
		}
		r, w, err := os.Pipe()
		if err != nil {
			st.closeAll()
			return nil, fmt.Errorf("readiness=fd: pipe: %w", err)
		}
		st.readEnd = r
		st.writeEnd = w
		cmd.ExtraFiles = append(cmd.ExtraFiles, w)
		return st, nil
	case "stderr":
		// The stderr line-scan mode reads the child's stderr, so any
		// pre-existing redirect would race with us.
		if opts.StderrLogger != "" || opts.Stderr != "" {
			return nil, fmt.Errorf("readiness=stderr conflicts with --stderr / --stderr-logger")
		}
		r, w, err := os.Pipe()
		if err != nil {
			return nil, fmt.Errorf("readiness=stderr: pipe: %w", err)
		}
		st.readEnd = r
		st.writeEnd = w
		cmd.Stderr = w
		return st, nil
	case "signal":
		// Install the notifier BEFORE fork so a fast child that
		// signals immediately after exec still lands in our channel.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, proto.signal)
		st.sigCh = ch
		return st, nil
	}
	// Unreachable — parseNotify covers all cases.
	return st, nil
}

// postFork releases the parent's copy of child-inherited fds so an EOF
// (from child closing its end, or exiting) becomes observable on the
// read side of the pipe. No-op for signal/none/pidfile/manual.
func (s *notifyState) postFork() {
	if s == nil {
		return
	}
	if s.writeEnd != nil {
		s.writeEnd.Close()
		s.writeEnd = nil
	}
	for _, p := range s.pads {
		p.Close()
	}
	s.pads = nil
}

// closeAll releases every open handle. Used on setup failure.
func (s *notifyState) closeAll() {
	if s.readEnd != nil {
		s.readEnd.Close()
		s.readEnd = nil
	}
	if s.writeEnd != nil {
		s.writeEnd.Close()
		s.writeEnd = nil
	}
	for _, p := range s.pads {
		p.Close()
	}
	s.pads = nil
	if s.sigCh != nil {
		signal.Stop(s.sigCh)
		s.sigCh = nil
	}
}

// wait blocks until the readiness protocol fires, the child dies, or
// notifyTimeout(opts) elapses. Returns an exit code.
func (s *notifyState) wait(opts Options, pid int) int {
	if s == nil {
		return exitOK
	}
	switch s.proto.mode {
	case "none", "manual":
		return exitOK
	case "pidfile":
		return waitPidfile(opts, pid)
	case "fd":
		return s.waitReadable(opts, pid, false)
	case "stderr":
		return s.waitReadable(opts, pid, true)
	case "signal":
		return s.waitSignal(opts, pid)
	default:
		fmt.Fprintf(os.Stderr, "--notify readiness=%s not implemented\n", s.proto.mode)
		return exitUnsupported
	}
}

// waitReadable is shared between fd:N (any byte = ready) and stderr
// (first non-empty line = ready). Poll ticks watch child liveness so
// an early crash doesn't hang the parent.
func (s *notifyState) waitReadable(opts Options, pid int, lineMode bool) int {
	defer func() {
		if s.readEnd != nil {
			s.readEnd.Close()
			s.readEnd = nil
		}
	}()
	timeout := notifyTimeout(opts)
	deadline := time.Now().Add(timeout)

	resultCh := make(chan int, 1)
	go func() {
		if lineMode {
			r := bufio.NewReader(s.readEnd)
			for {
				line, err := r.ReadString('\n')
				if strings.TrimSpace(line) != "" {
					resultCh <- exitOK
					return
				}
				if err != nil {
					// EOF before any content — child died silently.
					resultCh <- exitInsufficientPri
					return
				}
			}
		}
		buf := make([]byte, 128)
		n, _ := s.readEnd.Read(buf)
		if n > 0 {
			resultCh <- exitOK
			return
		}
		// EOF (all write ends closed) with no data.
		resultCh <- exitInsufficientPri
	}()

	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case code := <-resultCh:
			return code
		case <-tick.C:
			if !processAlive(pid) {
				s.readEnd.Close() // unblock the goroutine
				fmt.Fprintln(os.Stderr, "--notify: child exited before signalling readiness")
				return exitInsufficientPri
			}
			if !time.Now().Before(deadline) {
				s.readEnd.Close()
				fmt.Fprintf(os.Stderr, "--notify: readiness not signalled within %s\n", timeout)
				return exitInsufficientPri
			}
		}
	}
}

// waitSignal blocks on the pre-installed signal.Notify channel.
func (s *notifyState) waitSignal(opts Options, pid int) int {
	defer signal.Stop(s.sigCh)
	timeout := notifyTimeout(opts)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.sigCh:
			return exitOK
		case <-timer.C:
			fmt.Fprintf(os.Stderr, "--notify: signal %s not received within %s\n", s.proto.signal, timeout)
			return exitInsufficientPri
		case <-tick.C:
			if !processAlive(pid) {
				fmt.Fprintln(os.Stderr, "--notify: child exited before sending readiness signal")
				return exitInsufficientPri
			}
		}
	}
}

// notifyTimeout resolves the readiness deadline: honour --wait if the
// caller set one, otherwise fall back to a 30s ceiling that matches
// start-stop-daemon(8)'s default.
func notifyTimeout(opts Options) time.Duration {
	if opts.Wait > 0 {
		return time.Duration(opts.Wait) * time.Millisecond
	}
	return 30 * time.Second
}

// waitPidfile polls opts.PidFile at 100ms until it exists, the child
// dies, or the deadline elapses.
func waitPidfile(opts Options, pid int) int {
	if opts.PidFile == "" {
		fmt.Fprintln(os.Stderr, "--notify readiness=pidfile requires --pidfile")
		return exitBadUsage
	}
	timeout := notifyTimeout(opts)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(opts.PidFile); err == nil {
			return exitOK
		}
		if !processAlive(pid) {
			fmt.Fprintf(os.Stderr, "--notify pidfile: child exited before writing %q\n", opts.PidFile)
			return exitInsufficientPri
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(os.Stderr, "--notify pidfile: %q did not appear within %s\n", opts.PidFile, timeout)
	return exitInsufficientPri
}
