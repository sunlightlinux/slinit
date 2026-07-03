package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func cmdStop(opts Options) int {
	crit, err := matchCriteriaFrom(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitBadUsage
	}

	pids, err := FindMatchingPIDs(crit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scanning /proc: %v\n", err)
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
		// Debian default: single --signal, no timeout.
		return []Step{{Signal: opts.Signal, Timeout: 0}}, nil
	}
	return ParseRetry(opts.Retry, opts.Signal)
}

func runSchedule(pid int, schedule []Step, opts Options) error {
	for i, step := range schedule {
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
		if !waitExitProgress(pid, step.Timeout, opts.Progress) {
			// Timed out; move to next step. Last step timing out is a fail.
			if i == len(schedule)-1 {
				return fmt.Errorf("did not exit after %s within %s", signalName(step.Signal), step.Timeout)
			}
			continue
		}
		return nil
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
		fmt.Fprintf(os.Stderr, "scanning /proc: %v\n", err)
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

func lookupPrimaryGID(userSpec string) (int, error) {
	u, err := user.Lookup(userSpec)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Gid)
}

