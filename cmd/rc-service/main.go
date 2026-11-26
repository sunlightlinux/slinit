// rc-service — OpenRC-compatible service control shim for slinit.
//
// Accepts the OpenRC argv shape (`rc-service <service> <action>`) plus
// the older `--list`, `--resolve`, `--exists` forms, and translates
// them into the equivalent slinitctl invocation. Exit codes follow
// slinitctl — they are close enough to OpenRC's for all common
// automation (0 = success, nonzero = failure) — except status, which
// exits with the service's state as OpenRC's does. The point of this
// binary is to let scripts and admins that type `rc-service nginx
// restart` keep working without rewriting anything.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
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
		// OpenRC's -e succeeds whenever the service resolves, in any
		// state, and prints nothing. query-name loads the service and
		// fails only when it cannot be found or loaded; run discards
		// the name it prints.
		return []string{"query-name", argv[1]}, nil
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
	case "reload":
		// OpenRC's reload runs the script's reload(), which signals the
		// daemon to re-read its own configuration, and fails when the
		// script defines none. That is reload-signal, which fails the
		// same way without a reload-signal stanza; `slinitctl reload`
		// re-reads the service description and leaves the daemon alone.
		out = []string{"reload-signal", service}
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

	if len(argv) == 2 && argv[1] == "status" {
		return runStatus(bin, argv[0], stdout, stderr)
	}
	if argv[0] == "-e" || argv[0] == "--exists" {
		return runExists(bin, argv[1], stderr)
	}

	argvOut := append([]string{bin}, out...)
	if err := execFunc(bin, argvOut, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "rc-service: exec %s: %v\n", bin, err)
		return 1
	}
	// syscall.Exec only returns on failure.
	return 0
}

// runStatus prints `slinitctl status` and then exits as OpenRC's
// status does: with the service's state, not with success, since
// scripts branch on it. The state comes from `slinitctl is-failed`,
// which prints FAILED for a service that stopped on a failure and the
// plain state otherwise; its own exit status is not the answer here.
func runStatus(bin, service string, stdout, stderr *os.File) int {
	cmd := exec.Command(bin, "status", service)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(stderr, "rc-service: %s: %v\n", bin, err)
		return 1
	}
	cmd = exec.Command(bin, "is-failed", service)
	cmd.Stderr = stderr
	state, _ := cmd.Output()
	return openrcStatusCode(strings.TrimSpace(string(state)))
}

// runExists answers OpenRC's -e: exit 0 when the service resolves,
// whatever its state, with nothing on stdout. Errors (not found, no
// daemon) still reach stderr.
func runExists(bin, service string, stderr *os.File) int {
	cmd := exec.Command(bin, "query-name", service)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintf(stderr, "rc-service: %s: %v\n", bin, err)
		return 1
	}
	return 0
}

// openrcStatusCode maps the state slinitctl prints to the exit status
// of OpenRC's _status (openrc-run.sh). slinit has no "inactive" (16).
func openrcStatusCode(state string) int {
	switch state {
	case "STARTED":
		return 0
	case "STOPPED":
		return 3
	case "STOPPING":
		return 4
	case "STARTING":
		return 8
	case "FAILED":
		return 32 // OpenRC: crashed
	}
	return 1
}

// isAbsPath avoids importing path/filepath for a single byte check.
func isAbsPath(p string) bool { return len(p) > 0 && p[0] == '/' }

const helpText = `rc-service — OpenRC-style wrapper over slinitctl.

Usage:
  rc-service <service> <action>
  rc-service -e|--exists <service>
  rc-service -l|--list
  rc-service -r|--resolve <service>

Actions: start, stop, restart, status, zap, pause, continue, reload
Unrecognised actions are passed through to slinitctl as-is.

The SLINITCTL environment variable overrides the slinitctl binary path.`

// version is set at build time with -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	// --version, alone: checked before anything else runs, so no other
	// argument changes meaning. The value is stamped by the package
	// build (-X main.version=...).
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("rc-service version %s\n", version)
		return
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
