package util

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ParseDuration parses a duration string in seconds (decimal).
func ParseDuration(s string) (time.Duration, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %w", err)
	}
	return time.Duration(f * float64(time.Second)), nil
}

// ParseSignal parses a signal name (e.g., "SIGTERM", "TERM") or number.
func ParseSignal(s string) (syscall.Signal, error) {
	signals := map[string]syscall.Signal{
		"SIGHUP":  syscall.SIGHUP,
		"SIGINT":  syscall.SIGINT,
		"SIGQUIT": syscall.SIGQUIT,
		"SIGKILL": syscall.SIGKILL,
		"SIGTERM": syscall.SIGTERM,
		"SIGUSR1": syscall.SIGUSR1,
		"SIGUSR2": syscall.SIGUSR2,
	}

	upper := strings.ToUpper(s)
	if sig, ok := signals[upper]; ok {
		return sig, nil
	}
	if sig, ok := signals["SIG"+upper]; ok {
		return sig, nil
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("unknown signal: %s", s)
	}
	return syscall.Signal(n), nil
}
