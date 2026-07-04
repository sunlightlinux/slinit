package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// startLogger runs cmd as a subprocess with its stdin bound to the
// returned write end. Same behaviour as slinit-start-stop-daemon's
// stdio_logger.go: whitespace-split, no shell interpolation, background
// reap.
func startLogger(command string) (*os.File, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty logger command")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c := exec.Command(fields[0], fields[1:]...)
	c.Stdin = r
	// Logger inherits our stderr so operators see its diagnostics.
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	if err := c.Start(); err != nil {
		r.Close()
		w.Close()
		return nil, err
	}
	go func() {
		_ = c.Wait()
		r.Close()
	}()
	return w, nil
}
