package process

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PIDResult represents the outcome of reading a PID file.
type PIDResult int

const (
	// PIDResultOK means the PID was read successfully and the process exists.
	PIDResultOK PIDResult = iota
	// PIDResultFailed means the PID file could not be read or parsed.
	PIDResultFailed
	// PIDResultTerminated means the PID was valid but the process no longer exists.
	PIDResultTerminated
)

// ReadPIDFile reads a process ID from the given file path.
// It validates that the PID is a positive integer and checks if the process
// is still alive using kill(pid, 0).
func ReadPIDFile(path string) (int, PIDResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, PIDResultFailed, fmt.Errorf("reading PID file: %w", err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return 0, PIDResultFailed, errors.New("PID file is empty")
	}

	// PID file may contain PID on first line followed by other data
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		content = content[:idx]
	}

	pid, err := strconv.Atoi(strings.TrimSpace(content))
	if err != nil {
		return 0, PIDResultFailed, fmt.Errorf("invalid PID in file: %w", err)
	}

	if pid <= 0 {
		return 0, PIDResultFailed, fmt.Errorf("invalid PID value: %d", pid)
	}

	// Check if process exists
	err = syscall.Kill(pid, 0)
	if err == nil {
		return pid, PIDResultOK, nil
	}

	if errors.Is(err, syscall.ESRCH) {
		return pid, PIDResultTerminated, nil
	}

	// EPERM means the process exists but we don't have permission to signal it
	if errors.Is(err, syscall.EPERM) {
		return pid, PIDResultOK, nil
	}

	return pid, PIDResultFailed, fmt.Errorf("checking process %d: %w", pid, err)
}
