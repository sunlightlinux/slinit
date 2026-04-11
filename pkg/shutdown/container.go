package shutdown

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sunlightlinux/slinit/pkg/service"
)

const (
	// DefaultContainerResultsDir is where container exit info is written.
	DefaultContainerResultsDir = "/run/slinit/container-results"
)

// containerResultsDir can be overridden for testing or custom paths.
var containerResultsDir = DefaultContainerResultsDir

// SetContainerResultsDir overrides the results directory path.
func SetContainerResultsDir(dir string) {
	containerResultsDir = dir
}

// ContainerResultsDir returns the current container results directory.
func ContainerResultsDir() string {
	return containerResultsDir
}

// WriteContainerResults writes exit code and halt code files to the
// container results directory. This allows the container runtime or
// wrapper scripts to inspect how/why the container exited.
//
// Files written:
//   - exitcode:  numeric exit code (e.g. "0", "1", "137")
//   - haltcode:  shutdown type letter: "p" (poweroff), "r" (reboot),
//     "h" (halt), "s" (soft-reboot), "k" (kexec)
//
// Inspired by s6-linux-init's /run/s6-linux-init-container-results/.
func WriteContainerResults(exitCode int, shutdownType service.ShutdownType) error {
	dir := containerResultsDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// Write exitcode.
	ecPath := filepath.Join(dir, "exitcode")
	if err := os.WriteFile(ecPath, []byte(strconv.Itoa(exitCode)), 0644); err != nil {
		return fmt.Errorf("write exitcode: %w", err)
	}

	// Write haltcode.
	hc := haltCode(shutdownType)
	hcPath := filepath.Join(dir, "haltcode")
	if err := os.WriteFile(hcPath, []byte(hc), 0644); err != nil {
		return fmt.Errorf("write haltcode: %w", err)
	}

	return nil
}

// ReadContainerExitCode reads the exit code from the results directory.
// Returns 0 and false if the file does not exist or is unreadable.
func ReadContainerExitCode() (int, bool) {
	data, err := os.ReadFile(filepath.Join(containerResultsDir, "exitcode"))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

// ReadContainerHaltCode reads the halt code from the results directory.
// Returns empty string if the file does not exist.
func ReadContainerHaltCode() string {
	data, err := os.ReadFile(filepath.Join(containerResultsDir, "haltcode"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// haltCode maps a ShutdownType to a single-letter halt code.
func haltCode(st service.ShutdownType) string {
	switch st {
	case service.ShutdownPoweroff:
		return "p"
	case service.ShutdownReboot:
		return "r"
	case service.ShutdownHalt:
		return "h"
	case service.ShutdownSoftReboot:
		return "s"
	case service.ShutdownKexec:
		return "k"
	default:
		return "p" // default to poweroff
	}
}
