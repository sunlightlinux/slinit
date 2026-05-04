package logging

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	// DefaultCatchAllPath is the default location for the catch-all log file.
	DefaultCatchAllPath = "/run/slinit/catch-all.log"

	// catchAllMaxSize is the maximum size of the catch-all log before rotation.
	// Keeps memory/disk bounded for long-running systems.
	catchAllMaxSize = 1 * 1024 * 1024 // 1 MiB
)

// CatchAllLogger captures all output written to stdout/stderr (fd 1 and 2)
// by redirecting them through a pipe. A background goroutine reads from the
// pipe and writes each line to both the original console and a persistent
// log file. This ensures that early boot messages, panics from child
// processes, and any uncaught output is preserved.
//
// Inspired by s6-linux-init's catch-all logger (s6-svscan-log).
type CatchAllLogger struct {
	pipeR    *os.File // read end of the capture pipe
	pipeW    *os.File // write end (replaces fd 1 & 2)
	console  *os.File // original console (saved before redirect)
	logFile  *os.File // persistent log file
	logPath  string
	wg       sync.WaitGroup
	closeOnce sync.Once
}

// StartCatchAll sets up the catch-all logger. It:
//  1. Saves the current stdout as the console output
//  2. Creates a pipe
//  3. Redirects fd 1 and fd 2 to the pipe write end
//  4. Starts a goroutine that reads lines and tees them to console + file
//
// If logPath is empty, DefaultCatchAllPath is used.
// If logPath is "-" or the file cannot be created, only console output is used.
// Returns the CatchAllLogger (call Stop() to drain and close) or an error.
func StartCatchAll(logPath string) (*CatchAllLogger, error) {
	if logPath == "" {
		logPath = DefaultCatchAllPath
	}

	// Save current stdout fd (the console) before we replace it.
	// Dup it so we own the fd independently.
	consoleFD, err := syscall.Dup(1)
	if err != nil {
		return nil, fmt.Errorf("dup stdout: %w", err)
	}
	console := os.NewFile(uintptr(consoleFD), "/dev/console")

	// Create the pipe.
	pr, pw, err := os.Pipe()
	if err != nil {
		console.Close()
		return nil, fmt.Errorf("pipe: %w", err)
	}

	// Redirect fd 1 and fd 2 to the pipe write end.
	if err := syscall.Dup2(int(pw.Fd()), 1); err != nil {
		pr.Close()
		pw.Close()
		console.Close()
		return nil, fmt.Errorf("dup2 stdout: %w", err)
	}
	if err := syscall.Dup2(int(pw.Fd()), 2); err != nil {
		pr.Close()
		pw.Close()
		console.Close()
		return nil, fmt.Errorf("dup2 stderr: %w", err)
	}

	// Reassign os.Stdout and os.Stderr so Go's standard library uses the pipe.
	os.Stdout = os.NewFile(1, "/dev/stdout")
	os.Stderr = os.NewFile(2, "/dev/stderr")

	cal := &CatchAllLogger{
		pipeR:   pr,
		pipeW:   pw,
		console: console,
		logPath: logPath,
	}

	// Open persistent log file.
	if logPath != "-" {
		// Ensure parent directory exists.
		os.MkdirAll(filepath.Dir(logPath), 0755)
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0640)
		if err != nil {
			// Non-fatal: log only to console.
			fmt.Fprintf(console, "slinit: catch-all log file: %v (console only)\n", err)
		} else {
			cal.logFile = f
		}
	}

	// Start the reader goroutine.
	cal.wg.Add(1)
	go cal.drain()

	return cal, nil
}

// drain reads lines from the pipe and writes them to console and log file.
func (c *CatchAllLogger) drain() {
	defer c.wg.Done()

	scanner := bufio.NewScanner(c.pipeR)
	// Use a generous buffer for long lines (e.g. Go stack traces).
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var written int64

	for scanner.Scan() {
		line := scanner.Bytes()
		ts := time.Now().Format("2006-01-02T15:04:05.000")

		// Write to console (original stdout) — always.
		fmt.Fprintf(c.console, "%s\n", line)

		// Write to persistent log file with timestamp.
		if c.logFile != nil {
			n, _ := fmt.Fprintf(c.logFile, "%s %s\n", ts, line)
			written += int64(n)

			// Simple size-based rotation: truncate when too large.
			if written > catchAllMaxSize {
				c.logFile.Truncate(0)
				c.logFile.Seek(0, io.SeekStart)
				fmt.Fprintf(c.logFile, "%s [catch-all log rotated]\n", ts)
				written = 0
			}
		}
	}
}

// Console returns the original console writer. Use this for direct console
// output that bypasses the normal logging path (e.g. crash recovery messages).
func (c *CatchAllLogger) Console() *os.File {
	return c.console
}

// ReattachStdoutErr re-redirects fd 1 and fd 2 to the catch-all pipe. Call
// this after any code path that has performed its own Dup2 on those fds
// (notably InitPID1's setupConsole, which redirects them to /dev/console
// to ensure log output reaches the operator on systems without catch-all).
//
// Without this re-attach, messages logged BEFORE the un-redirect sit
// buffered in the pipe and the drain goroutine flushes them to the
// console LATER than subsequent direct-to-console writes, producing
// out-of-order timestamps. Calling Reattach restores the invariant
// "every log line goes through the pipe in chronological order".
func (c *CatchAllLogger) ReattachStdoutErr() error {
	if err := syscall.Dup2(int(c.pipeW.Fd()), 1); err != nil {
		return fmt.Errorf("dup2 stdout: %w", err)
	}
	if err := syscall.Dup2(int(c.pipeW.Fd()), 2); err != nil {
		return fmt.Errorf("dup2 stderr: %w", err)
	}
	os.Stdout = os.NewFile(1, "/dev/stdout")
	os.Stderr = os.NewFile(2, "/dev/stderr")
	return nil
}

// Stop drains remaining output from the pipe, closes the log file, and
// restores fd 1/2 to the original console. Safe to call multiple times.
func (c *CatchAllLogger) Stop() {
	c.closeOnce.Do(func() {
		// Close the write end so the drain goroutine gets EOF.
		// First restore fd 1/2 to console so further writes don't break.
		syscall.Dup2(int(c.console.Fd()), 1)
		syscall.Dup2(int(c.console.Fd()), 2)
		os.Stdout = os.NewFile(1, "/dev/stdout")
		os.Stderr = os.NewFile(2, "/dev/stderr")

		c.pipeW.Close()

		// Wait for drain to finish processing all buffered data.
		c.wg.Wait()

		c.pipeR.Close()
		if c.logFile != nil {
			c.logFile.Close()
		}
		c.console.Close()
	})
}
