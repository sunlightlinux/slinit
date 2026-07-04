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
// child-side hardening flag is set. Returns (execPath, argv, true, nil)
// when a wrap was applied, or ("", nil, false, nil) when no wrap is
// needed and the caller should exec `binary` with `argv` directly.
//
// supervise-daemon has no --startas / argv[0] override so unlike the
// start-stop-daemon variant this always presents `binary` as argv[0].
func runnerWrapArgs(opts Options, binary string, argv []string) (string, []string, bool, error) {
	if opts.Capabilities == "" && opts.Securebits == "" && !opts.NoNewPrivs {
		return "", nil, false, nil
	}
	runner, err := locateRunner()
	if err != nil {
		return "", nil, false, err
	}
	runnerArgs := []string{"slinit-runner"}
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
