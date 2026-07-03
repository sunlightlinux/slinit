package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/sunlightlinux/slinit/pkg/process"
)

// runnerWrapArgs prepends slinit-runner to the exec plan when any
// child-side hardening flag is set. Returns (execPath, argv, true,
// nil) when a wrap was applied, or ("", nil, false, nil) when no wrap
// is needed and the caller should exec `binary` with `argv` directly.
//
// The runner does `syscall.Exec(args[0], args, env)`, so it uses the
// first positional after `--` as both the binary path and argv[0].
// That means --startas (which sets a distinct argv[0]) is silently
// ignored when hardening flags are present; combining the two is a
// rare footgun anyway (hardening on start-stop-daemon vs a scripted
// re-exec via --startas).
func runnerWrapArgs(opts Options, binary string, argv []string) (string, []string, bool, error) {
	if opts.Capabilities == "" && opts.Securebits == "" && !opts.NoNewPrivs {
		return "", nil, false, nil
	}
	runner, err := locateRunner()
	if err != nil {
		return "", nil, false, err
	}
	runnerArgs := []string{"slinit-runner"} // argv[0] presented to runner
	if opts.NoNewPrivs {
		runnerArgs = append(runnerArgs, "--no-new-privs")
	}
	if opts.Capabilities != "" {
		caps, err := process.ParseCapabilities(opts.Capabilities)
		if err != nil {
			return "", nil, false, fmt.Errorf("--capabilities: %w", err)
		}
		for _, c := range caps {
			runnerArgs = append(runnerArgs, "--ambient-cap="+strconv.FormatUint(uint64(c), 10))
			runnerArgs = append(runnerArgs, "--bounding-cap="+strconv.FormatUint(uint64(c), 10))
		}
	}
	if opts.Securebits != "" {
		bits, err := process.ParseSecurebits(opts.Securebits)
		if err != nil {
			return "", nil, false, fmt.Errorf("--secbits: %w", err)
		}
		runnerArgs = append(runnerArgs, "--securebits="+strconv.FormatUint(uint64(bits), 10))
	}
	// Positional tail: real binary + user args. The runner exec's
	// runnerArgs[N] with itself as argv[0], so putting the binary path
	// here makes it the child's argv[0] too. --startas is ignored (see
	// note above).
	runnerArgs = append(runnerArgs, "--", binary)
	runnerArgs = append(runnerArgs, argv[1:]...)
	return runner, runnerArgs, true, nil
}

func locateRunner() (string, error) {
	if p, err := exec.LookPath("slinit-runner"); err == nil {
		return p, nil
	}
	self, err := os.Executable()
	if err == nil {
		sibling := filepath.Join(filepath.Dir(self), "slinit-runner")
		if _, err := os.Stat(sibling); err == nil {
			return sibling, nil
		}
	}
	return "", fmt.Errorf("slinit-runner not found on PATH; --capabilities/--secbits/--no-new-privs need it")
}
