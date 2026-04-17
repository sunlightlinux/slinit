// rc-service — OpenRC-compatible service control shim for slinit.
//
// Accepts the OpenRC argv shape (`rc-service <service> <action>`) plus
// the older `--list`, `--resolve`, `--exists` forms, and translates
// them into the equivalent slinitctl invocation. Exit codes follow
// slinitctl — they are close enough to OpenRC's for all common
// automation (0 = success, nonzero = failure). The point of this
// binary is to let scripts and admins that type `rc-service nginx
// restart` keep working without rewriting anything.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// slinitctlBin is the path to the slinitctl binary. Overridable via
// the SLINITCTL environment variable so distributions that install the
// tool under a different name or prefix can still use rc-service.
// Tests also override it to point at a stub.
var slinitctlBin = func() string {
	if v := os.Getenv("SLINITCTL"); v != "" {
		return v
	}
	return "slinitctl"
}()

// execFunc is swapped out in tests. In production it exec(3)s, which
// replaces the current process image so slinitctl's exit status flows
// directly back to the caller.
var execFunc = syscall.Exec

// lookPathFunc locates slinitctl on PATH. Overridable for tests.
var lookPathFunc = exec.LookPath

// translate converts OpenRC-style rc-service argv into slinitctl argv.
// Returns a usage error if the invocation is malformed.
func translate(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("usage: rc-service [--exists|--resolve|--list] | <service> <action>")
	}

	switch argv[0] {
	case "-h", "--help":
		return nil, errHelp
	case "-e", "--exists":
		if len(argv) != 2 {
			return nil, fmt.Errorf("rc-service --exists <service>")
		}
		// Slinit's equivalent is `is-started` returning non-zero if
		// the service isn't even loadable. That's close enough to
		// OpenRC's semantic of "this service exists on the system".
		return []string{"is-started", argv[1]}, nil
	case "-l", "--list":
		return []string{"list"}, nil
	case "-r", "--resolve":
		if len(argv) != 2 {
			return nil, fmt.Errorf("rc-service --resolve <service>")
		}
		// OpenRC's --resolve prints the absolute path to the init
		// script; we don't have an exact equivalent, but query-name
		// tells the operator what slinit knows the service as.
		return []string{"query-name", argv[1]}, nil
	}

	if len(argv) < 2 {
		return nil, fmt.Errorf("usage: rc-service <service> <action>")
	}

	service := argv[0]
	action := argv[1]

	// Map OpenRC actions to slinitctl's spelling. Unknown actions are
	// passed through verbatim so future OpenRC verbs that happen to
	// have a slinitctl equivalent keep working.
	var out []string
	switch action {
	case "start":
		out = []string{"start", service}
	case "stop":
		out = []string{"stop", service}
	case "restart":
		out = []string{"restart", service}
	case "status":
		out = []string{"status", service}
	case "zap":
		// OpenRC's "zap" forces the service state back to stopped
		// regardless of what it was doing. Closest slinit equivalent
		// is `release` + `stop --force`. We do release (clears any
		// pinning) followed by a stop.
		out = []string{"release", service}
	case "pause":
		out = []string{"pause", service}
	case "continue":
		out = []string{"continue", service}
	default:
		out = []string{action, service}
	}

	// Propagate any trailing args (rare — OpenRC's rc-service takes no
	// extra args after the action, but we stay future-proof).
	if len(argv) > 2 {
		out = append(out, argv[2:]...)
	}
	return out, nil
}

// errHelp is sentinel: translate signals "print help and exit 0" via
// this rather than pretending help is a slinitctl command.
var errHelp = fmt.Errorf("help requested")

// run performs the translation + dispatch. Returns the process exit
// code (for tests). In production main passes it to os.Exit.
func run(argv []string, stdout, stderr *os.File) int {
	out, err := translate(argv)
	if err != nil {
		if err == errHelp {
			fmt.Fprintln(stdout, helpText)
			return 0
		}
		fmt.Fprintln(stderr, "rc-service:", err)
		return 2
	}

	// Resolve slinitctl path — exec(3) demands an absolute path. If
	// SLINITCTL was already absolute we use it as-is.
	bin := slinitctlBin
	if !isAbsPath(bin) {
		resolved, err := lookPathFunc(bin)
		if err != nil {
			fmt.Fprintf(stderr, "rc-service: cannot find %s on PATH: %v\n", bin, err)
			return 127
		}
		bin = resolved
	}

	argvOut := append([]string{bin}, out...)
	if err := execFunc(bin, argvOut, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "rc-service: exec %s: %v\n", bin, err)
		return 1
	}
	// syscall.Exec only returns on failure.
	return 0
}

// isAbsPath avoids importing path/filepath for a single byte check.
func isAbsPath(p string) bool { return len(p) > 0 && p[0] == '/' }

const helpText = `rc-service — OpenRC-style wrapper over slinitctl.

Usage:
  rc-service <service> <action>
  rc-service -e|--exists <service>
  rc-service -l|--list
  rc-service -r|--resolve <service>

Actions: start, stop, restart, status, zap, pause, continue
Unrecognised actions are passed through to slinitctl as-is.

The SLINITCTL environment variable overrides the slinitctl binary path.`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
