package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
)

func cmdStop(opts Options) int {
	crit, err := matchCriteriaFrom(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitBadUsage
	}

	pids, err := FindMatchingPIDs(crit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInsufficientPri
	}
	if len(pids) == 0 {
		writeMsg(opts, "no matching process")
		if opts.OKnodo || opts.PidFile == "" {
			return exitOK
		}
		// Pidfile was named but its target is gone: LSB "5 not running".
		if _, err := os.Stat(opts.PidFile); err == nil {
			return exitStalePidfile
		}
		return exitOK
	}

	if opts.Test {
		writeMsg(opts, "would stop pids %v with %s", pids, signalName(opts.Signal))
		return exitOK
	}

	schedule, err := buildSchedule(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitBadUsage
	}

	final := exitOK
	for _, pid := range pids {
		if err := runSchedule(pid, schedule, opts); err != nil {
			fmt.Fprintf(os.Stderr, "stop pid=%d: %v\n", pid, err)
			final = exitInsufficientPri
		}
	}

	// Best-effort pidfile cleanup once the last matching pid is gone.
	if opts.PidFile != "" && final == exitOK {
		if pid, err := readPIDFile(opts.PidFile); err == nil && pid > 0 && !processAlive(pid) {
			_ = os.Remove(opts.PidFile)
		}
	}
	return final
}

func buildSchedule(opts Options) ([]Step, error) {
	if opts.Retry == "" {
		// OpenRC's default schedule: the signal, then up to 5 s for
		// the process to go. With --oknodo, as OpenRC's schedule "0",
		// send it and return. A zero Timeout means "wait forever".
		if opts.OKnodo {
			return []Step{{Signal: opts.Signal, Timeout: noWait}}, nil
		}
		return []Step{{Signal: opts.Signal, Timeout: 5 * time.Second}}, nil
	}
	return ParseRetry(opts.Retry, opts.Signal)
}

func runSchedule(pid int, schedule []Step, opts Options) error {
	repeatAt := -1
	for i := 0; i < len(schedule); i++ {
		step := schedule[i]
		if step.Repeat {
			repeatAt = i + 1
			continue
		}
		// Last step of the schedule, and nothing to start again from.
		last := i == len(schedule)-1 && repeatAt < 0
		if step.Signal != 0 {
			if opts.Verbose {
				writeMsg(opts, "sending %s to pid=%d", signalName(step.Signal), pid)
			}
			if err := syscall.Kill(pid, step.Signal); err != nil {
				if err == syscall.ESRCH {
					return nil
				}
				if err == syscall.EPERM {
					return fmt.Errorf("kill: %w", err)
				}
				return fmt.Errorf("kill(%s): %w", signalName(step.Signal), err)
			}
		}
		switch step.Timeout {
		case noWait:
			return nil
		case sendOnly:
			if !last {
				break
			}
			if waitExitProgress(pid, sendOnlyGrace, false) {
				return nil
			}
			return fmt.Errorf("did not exit after %s (end of --retry schedule)", signalName(step.Signal))
		default:
			if waitExitProgress(pid, step.Timeout, opts.Progress) {
				return nil
			}
			// Timed out; move to next step. Last step timing out is a fail.
			if last {
				return fmt.Errorf("did not exit after %s within %s", signalName(step.Signal), step.Timeout)
			}
		}
		if i == len(schedule)-1 && repeatAt >= 0 {
			i = repeatAt - 1 // the loop's i++ lands on repeatAt
		}
	}
	return nil
}

func cmdStatus(opts Options) int {
	crit, err := matchCriteriaFrom(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitBadUsage
	}
	pids, err := FindMatchingPIDs(crit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitInsufficientPri
	}
	if len(pids) > 0 {
		if !opts.Quiet {
			for _, p := range pids {
				fmt.Println(p)
			}
		}
		return exitOK
	}
	// LSB semantics: PID file exists but process gone → 1; otherwise 3.
	if opts.PidFile != "" {
		if _, err := os.Stat(opts.PidFile); err == nil {
			return exitAlready
		}
	}
	return exitUnsupported
}

func signalName(sig syscall.Signal) string {
	for name, s := range signalNames {
		if s == sig {
			return "SIG" + name
		}
	}
	return strconv.Itoa(int(sig))
}

// lookupPrimaryGID is the passwd gid of a user name or numeric uid
// (--chuid 51 took the caller's gid, root's, when only names resolved).
func lookupPrimaryGID(userSpec string) (int, error) {
	u, err := lookupUser(userSpec)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Gid)
}
