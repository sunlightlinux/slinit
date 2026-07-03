package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// startLogger fires up a background process that reads from the
// returned *os.File (write end of a pipe). The caller wires that
// write end into cmd.Stdout or cmd.Stderr. The logger is orphaned to
// slinit-managed reaping — it exits when the child closes the write
// end and it reads EOF.
//
// Simple whitespace split for the command line: matches OpenRC's own
// behaviour, which does not evaluate shell metacharacters.
func startLogger(spec string) (*os.File, error) {
	parts := strings.Fields(spec)
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty logger command")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	logger := exec.Command(parts[0], parts[1:]...)
	logger.Stdin = r
	logger.Stdout = os.Stdout
	logger.Stderr = os.Stderr
	if err := logger.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, fmt.Errorf("start logger %q: %w", parts[0], err)
	}
	// Close our copy of the read end so the logger's EOF is delivered
	// when the child closes the write end.
	r.Close()
	// Reap so we do not leave a zombie when the write side closes.
	go func() { _ = logger.Wait() }()
	return w, nil
}
