// rc-update — OpenRC-compatible runlevel membership tool for slinit.
//
// Runlevels aren't a native slinit concept — we model them as ordinary
// services named runlevel-<name> whose `waits-for` dependencies are
// the members of that runlevel. So `rc-update add nginx default`
// becomes `slinitctl --from runlevel-default enable nginx`, and the
// resulting graph boots nginx whenever someone starts runlevel-default
// (including via `init N` aliases).
//
// Supported runlevels follow OpenRC's conventions: sysinit, boot,
// default, shutdown, nonetwork. Unknown names are accepted verbatim,
// letting admins define their own runlevel-<foo> services.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// slinitctlBin + override hooks mirror rc-service so the two binaries
// stay easy to reason about.
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

// defaultRunlevel is what OpenRC uses when the admin omits the
// runlevel argument. Picked to match `rc-update` defaults so operators
// don't get a surprise.
const defaultRunlevel = "default"

// runlevelService maps an OpenRC runlevel name to the slinit service
// that owns its waits-for graph. The mapping is trivial (prefix with
// "runlevel-") but pulled into a helper so we can reason about
// reserved names in one place.
func runlevelService(name string) string {
	return "runlevel-" + name
}

// translate converts `rc-update <verb> <args>` into the equivalent
// slinitctl argv. Returns (nil, errHelp) when the caller asked for
// help; (nil, error) on a usage error.
func translate(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return nil, errHelp
	}

	verb := argv[0]
	switch verb {
	case "-h", "--help":
		return nil, errHelp

	case "add":
		svc, level, err := parseAddDel(argv[1:])
		if err != nil {
			return nil, err
		}
		// `slinitctl --from runlevel-X enable svc` adds a waits-for
		// edge from runlevel-X to svc. slinit's enable is persistent
		// (writes a symlink under the service dir) which matches
		// rc-update's "sticks across reboots" semantics.
		return []string{"--from", runlevelService(level), "enable", svc}, nil

	case "del", "delete":
		svc, level, err := parseAddDel(argv[1:])
		if err != nil {
			return nil, err
		}
		return []string{"--from", runlevelService(level), "disable", svc}, nil

	case "show":
		level := defaultRunlevel
		if len(argv) > 1 {
			level = argv[1]
		}
		// `slinitctl graph runlevel-X` prints the dependency graph
		// rooted at the runlevel, which lists every member with its
		// dep type. It's the closest equivalent to `rc-update show`.
		return []string{"graph", runlevelService(level)}, nil

	case "update", "-u":
		// OpenRC rebuilds its boot-time dep cache here. slinit keeps
		// no such cache (deps are resolved live from the service
		// descriptions), so this is a no-op. Report success so
		// scripts that blindly call `rc-update -u` don't fail.
		return nil, errNoop

	default:
		// Unknown verb — pass through to slinitctl verbatim so admins
		// can discover what's actually supported.
		return argv, nil
	}
}

// parseAddDel pulls out (service, runlevel) from `rc-update add foo
// [default]` and its `del` sibling. Defaults to the `default`
// runlevel when the admin omits it.
func parseAddDel(args []string) (service, runlevel string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("missing <service> argument")
	}
	if len(args) > 2 {
		return "", "", fmt.Errorf("too many arguments: got %v, expected <service> [<runlevel>]", args)
	}
	svc := args[0]
	level := defaultRunlevel
	if len(args) == 2 {
		level = args[1]
	}
	if svc == "" || strings.ContainsAny(svc, "/\x00") {
		return "", "", fmt.Errorf("invalid service name %q", svc)
	}
	if level == "" || strings.ContainsAny(level, "/\x00") {
		return "", "", fmt.Errorf("invalid runlevel name %q", level)
	}
	return svc, level, nil
}

var (
	errHelp = fmt.Errorf("help requested")
	errNoop = fmt.Errorf("no-op")
)

func run(argv []string, stdout, stderr *os.File) int {
	out, err := translate(argv)
	if err == errHelp {
		fmt.Fprintln(stdout, helpText)
		return 0
	}
	if err == errNoop {
		// `rc-update update` — nothing to do, report success.
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "rc-update:", err)
		return 2
	}

	bin := slinitctlBin
	if len(bin) == 0 || bin[0] != '/' {
		resolved, err := lookPathFunc(bin)
		if err != nil {
			fmt.Fprintf(stderr, "rc-update: cannot find %s on PATH: %v\n", bin, err)
			return 127
		}
		bin = resolved
	}

	argvOut := append([]string{bin}, out...)
	if err := execFunc(bin, argvOut, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "rc-update: exec %s: %v\n", bin, err)
		return 1
	}
	return 0
}

const helpText = `rc-update — OpenRC-style runlevel membership tool for slinit.

Usage:
  rc-update add    <service> [runlevel]    Register service in runlevel
  rc-update del    <service> [runlevel]    Remove service from runlevel
  rc-update show   [runlevel]              List services in runlevel
  rc-update update                         No-op (slinit has no dep cache)

Default runlevel is "default". Runlevels map to services named
runlevel-<name>, which you define with ordinary slinit service
descriptions. Known OpenRC names: sysinit, boot, default, shutdown,
nonetwork.

The SLINITCTL environment variable overrides the slinitctl binary path.`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
