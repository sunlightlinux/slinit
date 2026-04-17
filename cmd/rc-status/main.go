// rc-status — OpenRC-compatible service status listing for slinit.
//
// With no args, prints every service grouped by the runlevel service
// that depends on it. With a single argument, it is treated as a
// runlevel name and only that group is printed. Under the hood this
// is a thin projection over `slinitctl list` — the native protocol
// already supplies every state bit we need.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

var slinitctlBin = func() string {
	if v := os.Getenv("SLINITCTL"); v != "" {
		return v
	}
	return "slinitctl"
}()

var (
	execFunc     = syscall.Exec
	lookPathFunc = exec.LookPath
)

// translate returns the slinitctl argv used to satisfy an rc-status
// invocation. The translations are deliberately minimal:
//   - no args      → `slinitctl list`
//   - <runlevel>   → `slinitctl graph runlevel-<name>`
//   - -l | --list  → print known runlevel names (handled in run)
//   - -a | --all   → same as no args
//   - -r | --runlevel → print current runlevel (handled in run)
//
// OpenRC's rc-status prints a beautifully formatted per-runlevel
// grouping with coloured OK/STOPPED markers. We don't replicate the
// formatting — `slinitctl list` already distinguishes states with its
// own conventions, and forcing OpenRC's colour codes through would be
// a maintenance burden. Operators who want the exact OpenRC look can
// script it on top of `slinitctl list5`.
func translate(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return []string{"list"}, nil
	}

	switch argv[0] {
	case "-h", "--help":
		return nil, errHelp
	case "-l", "--list":
		return nil, errListRunlevels
	case "-r", "--runlevel":
		return nil, errCurrentRunlevel
	case "-a", "--all":
		return []string{"list"}, nil
	case "-s", "--servicelist":
		return []string{"list"}, nil
	case "-u", "--unused":
		// OpenRC lists services not in any runlevel; we don't have a
		// direct equivalent short of walking every dep graph. For
		// MVP, fall through to the full list so the admin can at
		// least see what's there.
		return []string{"list"}, nil
	}

	if argv[0][0] == '-' {
		return nil, fmt.Errorf("unknown flag %q", argv[0])
	}

	// Assume a bare positional arg is a runlevel name.
	return []string{"graph", "runlevel-" + argv[0]}, nil
}

// openrcRunlevels is the canonical set of runlevel names we advertise
// when the admin asks for `rc-status --list`. Custom runlevels defined
// by the admin still work (rc-update add foo myrunlevel); we just
// don't enumerate them here because discovering custom runlevel-*
// services requires a full `slinitctl list` round-trip.
var openrcRunlevels = []string{"sysinit", "boot", "default", "nonetwork", "shutdown"}

var (
	errHelp             = fmt.Errorf("help requested")
	errListRunlevels    = fmt.Errorf("list runlevels")
	errCurrentRunlevel  = fmt.Errorf("current runlevel")
)

func run(argv []string, stdout, stderr *os.File) int {
	out, err := translate(argv)
	switch err {
	case errHelp:
		fmt.Fprintln(stdout, helpText)
		return 0
	case errListRunlevels:
		for _, r := range openrcRunlevels {
			fmt.Fprintln(stdout, r)
		}
		return 0
	case errCurrentRunlevel:
		// slinit has no concept of a "current" runlevel. Report
		// "default" as a stable stand-in so scripts that depend on
		// the value keep working — matches the steady-state a fully
		// booted OpenRC system would report.
		fmt.Fprintln(stdout, "default")
		return 0
	case nil:
		// fall through to exec
	default:
		fmt.Fprintln(stderr, "rc-status:", err)
		return 2
	}

	bin := slinitctlBin
	if len(bin) == 0 || bin[0] != '/' {
		resolved, err := lookPathFunc(bin)
		if err != nil {
			fmt.Fprintf(stderr, "rc-status: cannot find %s on PATH: %v\n", bin, err)
			return 127
		}
		bin = resolved
	}

	argvOut := append([]string{bin}, out...)
	if err := execFunc(bin, argvOut, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "rc-status: exec %s: %v\n", bin, err)
		return 1
	}
	return 0
}

const helpText = `rc-status — OpenRC-style status shim for slinit.

Usage:
  rc-status                         List all services (runs slinitctl list)
  rc-status <runlevel>              Show services that make up <runlevel>
  rc-status -l|--list               List known runlevel names
  rc-status -r|--runlevel           Print current runlevel
  rc-status -a|--all                Same as no args
  rc-status -s|--servicelist        Same as no args

The SLINITCTL environment variable overrides the slinitctl binary path.`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
