package logging

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultCatchAllPath is the default location for the catch-all log file.
	DefaultCatchAllPath = "/run/slinit/catch-all.log"

	// catchAllMaxSize is the maximum size of the catch-all log before rotation.
	// Keeps memory/disk bounded for long-running systems.
	catchAllMaxSize = 1 * 1024 * 1024 // 1 MiB

	// The console tee's queue: lines waiting for a console that is slower
	// than the pipe, or stopped. When it is full the drain waits for the
	// console, as it did when it wrote to it itself. The byte bound keeps
	// what a slow console still has to write at Stop near what the pipe
	// held before the queue existed (64 KiB), so the queue adds ~6 s at
	// 115200 baud to a flush, not ~90 s.
	catchAllConsoleQueueLines = 1024
	catchAllConsoleQueueBytes = 64 * 1024

	// catchAllConsoleFlushStall is how long the drain waits for a console
	// that takes no line at all before dropping lines for it. A slow but
	// working console takes a line well within it.
	catchAllConsoleFlushStall = time.Second

	// catchAllConsoleFlushMax bounds how long Stop (soft reboot, exit)
	// waits for a slow console to write what it is still owed; what it
	// has not written by then is dropped.
	catchAllConsoleFlushMax = 5 * time.Second
)

// CatchAllLogger captures all output written to stdout/stderr (fd 1 and 2)
// by redirecting them through a pipe. A background goroutine reads from the
// pipe and writes each line to both the original console and a persistent
// log file. This ensures that early boot messages, panics from child
// processes, and any uncaught output is preserved.
//
// Inspired by s6-linux-init's catch-all logger (s6-svscan-log).
type CatchAllLogger struct {
	pipeR   *os.File // read end of the capture pipe
	pipeW   *os.File // write end (replaces fd 1 & 2)
	console *os.File // original console (saved before redirect)

	// consoleEOL ends each line on the console: "\r\n" on a terminal, as
	// writeConsole explains. The scanner below strips the "\r" of a line
	// that had one, so it has to be put back here; written with a bare
	// "\n", the shutdown lines of PID 1 staircased under sudo's raw mode
	// although the logger wrote them correctly. A pipe keeps "\n".
	consoleEOL string

	logFile   *os.File // persistent log file
	logPath   string
	wg        sync.WaitGroup
	closeOnce sync.Once
	// consoleMuted, when true, suppresses the console tee — every
	// captured line still lands in logFile, but the console side is
	// skipped. cmd/slinit flips this on once the boot service reaches
	// STARTED so a chatty demo (or any workload with many stdout-
	// echoing services) doesn't race the interactive tty for the
	// serial line during runtime. Atomic access so drain() and the
	// setter don't need to share a mutex.
	consoleMuted atomic.Bool

	// The console tee runs in a goroutine of its own, fed by the drain
	// through consoleQ. The drain wrote to the console itself until a
	// stopped console (Ctrl-S, Scroll Lock, XOFF) blocked it: the pipe
	// filled behind it and PID 1 blocked writing its own stderr, with
	// the scheduling lock held. consoleQueued is the bytes in consoleQ.
	// consoleLines counts the lines written and consoleWrote is signalled
	// after each, so the drain can tell a slow console, which it waits
	// for, from a stopped one, which it drops lines for.
	// consoleDropped (lines dropped since one was last queued),
	// consoleStalled and stalledAt (consoleLines when the drain gave up
	// waiting) are the drain's alone. flushBy, set by Stop, is the
	// deadline (UnixNano) past which the drain no longer waits at all.
	consoleQ       chan []byte
	consoleQueued  atomic.Int64
	consoleDropped uint64
	consoleStalled bool
	stalledAt      uint64
	consoleLines   atomic.Uint64
	consoleWrote   chan struct{}
	consoleDone    chan struct{}
	flushBy        atomic.Int64
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
	// Dup it so we own the fd independently, and close-on-exec: a plain
	// dup(2) is inherited by every process slinit starts, since os/exec
	// closes nothing it was not given, and under PID 1 that handed every
	// service, however unprivileged, a writable /dev/console. Nothing
	// needs this fd across exec: a soft reboot hands the console over on
	// fd 1/2 (Stop dup2's it there, which clears close-on-exec).
	consoleFD, err := unix.FcntlInt(1, unix.F_DUPFD_CLOEXEC, 0)
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
	renewStdio()

	cal := &CatchAllLogger{
		pipeR:      pr,
		pipeW:      pw,
		console:    console,
		consoleEOL: "\n",
		logPath:    logPath,
	}
	if _, err := unix.IoctlGetTermios(int(console.Fd()), unix.TCGETS); err == nil {
		cal.consoleEOL = "\r\n"
	}

	// Open persistent log file.
	if logPath != "-" {
		// Ensure parent directory exists.
		os.MkdirAll(filepath.Dir(logPath), 0755)
		f, err := OpenAppend(logPath, 0640)
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

// catchAllMaxLine is the longest line the drain passes on in one piece
// (Go stack traces are long); a longer one goes out in pieces this size.
const catchAllMaxLine = 256 * 1024

// drain reads lines from the pipe and writes them to console and log file.
//
// It reads until EOF whatever the lines look like. A bufio.Scanner gave
// up on the first line over its limit, the drain returned, nobody read
// the pipe again, and once it filled PID 1 blocked writing its own
// stderr.
func (c *CatchAllLogger) drain() {
	defer c.wg.Done()
	c.startConsoleTee()
	defer c.stopConsoleTee()

	r := bufio.NewReaderSize(c.pipeR, 64*1024)
	var (
		written int64
		pending []byte
	)
	for {
		frag, err := r.ReadSlice('\n')
		pending = append(pending, frag...)
		switch {
		case err == nil:
			// A complete line: drop "\n" and a "\r" before it, as
			// bufio.ScanLines did.
			line := bytes.TrimSuffix(pending[:len(pending)-1], []byte("\r"))
			c.handleLine(line, &written)
			pending = pending[:0]
		case err == bufio.ErrBufferFull:
			if len(pending) >= catchAllMaxLine {
				c.handleLine(pending, &written)
				pending = pending[:0]
			}
		default:
			if len(pending) > 0 {
				c.handleLine(pending, &written)
			}
			return
		}
	}
}

// handleLine tees one line to the console and the log file. written is
// the drain's running count of bytes in the log file.
func (c *CatchAllLogger) handleLine(line []byte, written *int64) {
	// The marker can land behind a child's unterminated output: what
	// precedes it is still a line, and the mute applies after it.
	mute := bytes.HasSuffix(line, []byte(muteMarker))
	if mute {
		line = line[:len(line)-len(muteMarker)]
		if len(line) == 0 {
			c.consoleMuted.Store(true)
			return
		}
	}
	ts := time.Now().Format("2006-01-02T15:04:05.000")

	// Write to console (original stdout) unless the operator (or
	// cmd/slinit main) has muted the console tee — see
	// SetConsoleMuted. File tee below always fires so the full
	// stream stays in /run/slinit/catch-all.log for post-hoc
	// inspection.
	if !c.consoleMuted.Load() {
		c.toConsole(line)
	}

	// Write to persistent log file with timestamp.
	if c.logFile != nil {
		n, _ := fmt.Fprintf(c.logFile, "%s %s\n", ts, line)
		*written += int64(n)

		// Simple size-based rotation: truncate when too large.
		if *written > catchAllMaxSize {
			c.logFile.Truncate(0)
			c.logFile.Seek(0, io.SeekStart)
			fmt.Fprintf(c.logFile, "%s [catch-all log rotated]\n", ts)
			*written = 0
		}
	}
	if mute {
		c.consoleMuted.Store(true)
	}
}

// startConsoleTee starts the goroutine that writes queued lines to the
// console.
func (c *CatchAllLogger) startConsoleTee() {
	c.consoleQ = make(chan []byte, catchAllConsoleQueueLines)
	c.consoleWrote = make(chan struct{}, 1)
	c.consoleDone = make(chan struct{})
	go func() {
		defer close(c.consoleDone)
		for b := range c.consoleQ {
			c.console.Write(b)
			c.consoleQueued.Add(-int64(len(b)))
			c.consoleLines.Add(1)
			select {
			case c.consoleWrote <- struct{}{}:
			default:
			}
		}
	}()
}

// toConsole queues one line for the console. When the queue is full it
// waits for the console to take lines, for as long as it keeps taking
// them, and drops the line only once it has taken none for
// catchAllConsoleFlushStall; after that, lines are dropped without
// waiting until the console takes one again. The first line queued after
// a drop is preceded by a line saying how many were lost. A console that
// takes its output, however slowly, gets every line, as when the drain
// wrote to it directly.
func (c *CatchAllLogger) toConsole(line []byte) {
	b := make([]byte, 0, len(line)+len(c.consoleEOL))
	if c.consoleDropped > 0 {
		b = fmt.Appendf(b, "slinit: the console was blocked; %d lines of output to it were dropped%s",
			c.consoleDropped, c.consoleEOL)
	}
	b = append(b, line...)
	b = append(b, c.consoleEOL...)
	if !c.waitForRoom(int64(len(b))) {
		c.consoleDropped++
		return
	}
	// The drain is consoleQ's only sender and there is room: this does
	// not block.
	c.consoleQueued.Add(int64(len(b)))
	c.consoleQ <- b
	c.consoleDropped = 0
}

// full reports whether n more bytes would overfill the queue. A line
// longer than the byte bound fits an empty queue.
func (c *CatchAllLogger) full(n int64) bool {
	if len(c.consoleQ) == cap(c.consoleQ) {
		return true
	}
	q := c.consoleQueued.Load()
	return q > 0 && q+n > catchAllConsoleQueueBytes
}

// waitForRoom waits until n bytes fit the queue, while the console keeps
// taking lines; it reports false when the console has taken none for
// catchAllConsoleFlushStall, or Stop's deadline has passed.
func (c *CatchAllLogger) waitForRoom(n int64) bool {
	if c.consoleStalled && c.consoleLines.Load() != c.stalledAt {
		c.consoleStalled = false
	}
	if !c.full(n) {
		return true
	}
	if c.consoleStalled {
		return false
	}
	var timer *time.Timer
	for c.full(n) {
		wait := catchAllConsoleFlushStall
		if by := c.flushBy.Load(); by != 0 {
			left := time.Until(time.Unix(0, by))
			if left <= 0 {
				return false
			}
			wait = min(wait, left)
		}
		if timer == nil {
			timer = time.NewTimer(wait)
			defer timer.Stop()
		} else {
			timer.Reset(wait)
		}
		last := c.consoleLines.Load()
		select {
		case <-c.consoleWrote:
		case <-timer.C:
			if now := c.consoleLines.Load(); now == last {
				c.consoleStalled, c.stalledAt = true, now
				return false
			}
		}
	}
	return true
}

// stopConsoleTee lets the console tee write what is queued, for as long
// as the console keeps taking lines and Stop's deadline allows. A
// stopped one is left behind with its goroutine: Stop, and soft reboot
// and exit behind it, used to wait on it for as long as it stayed
// stopped.
func (c *CatchAllLogger) stopConsoleTee() {
	close(c.consoleQ)
	last := c.consoleLines.Load()
	for {
		wait := catchAllConsoleFlushStall
		if by := c.flushBy.Load(); by != 0 {
			wait = min(wait, time.Until(time.Unix(0, by)))
		}
		if wait <= 0 {
			return
		}
		select {
		case <-c.consoleDone:
			return
		case <-time.After(wait):
		}
		n := c.consoleLines.Load()
		if n == last {
			return
		}
		last = n
	}
}

// beginFlush starts Stop's bound on the wait for a slow console.
func (c *CatchAllLogger) beginFlush() {
	c.flushBy.Store(time.Now().Add(catchAllConsoleFlushMax).UnixNano())
}

// Console returns the original console writer. Use this for direct console
// output that bypasses the normal logging path (e.g. crash recovery messages).
func (c *CatchAllLogger) Console() *os.File {
	return c.console
}

// muteMarker, passing through the pipe, mutes the console tee. No real line
// looks like it: a log line cannot start with a NUL.
const muteMarker = "\x00slinit-catch-all-mute"

// SetConsoleMuted toggles the console-side of the tee. When true,
// captured stdout+stderr lines are still written to the persistent
// log file but skipped for the console. cmd/slinit flips this on
// once the boot service reaches STARTED so an interactive tty is
// not swamped by workload echoes — the boot phase still gets its
// console mirror. Concurrent-safe.
//
// Muting goes through the pipe, so it takes effect after what was written
// before it. Set directly, it overtook the lines still in the pipe, and the
// last "[ OK ]" of the boot, the service whose start made boot ready, never
// reached the screen.
func (c *CatchAllLogger) SetConsoleMuted(v bool) {
	if c == nil {
		return
	}
	if v && c.pipeW != nil {
		if _, err := fmt.Fprintln(c.pipeW, muteMarker); err == nil {
			return
		}
	}
	c.consoleMuted.Store(v)
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
	renewStdio()
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
		renewStdio()

		c.beginFlush()
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

// retiredStdio holds every *os.File that os.Stdout or os.Stderr was
// before renewStdio replaced it. They are never closed: each names fd 1
// or 2, which stay in use, and an *os.File that becomes garbage closes
// its descriptor when finalized. Dropping them let the next GC close
// fd 1 and 2, under PID 1 the catch-all pipe.
var (
	retiredMu    sync.Mutex
	retiredStdio []*os.File
)

// renewStdio points os.Stdout and os.Stderr at fresh *os.File values for
// fd 1 and 2 after a dup2 onto them, keeping the old ones reachable.
func renewStdio() {
	retiredMu.Lock()
	retiredStdio = append(retiredStdio, os.Stdout, os.Stderr)
	retiredMu.Unlock()
	os.Stdout = os.NewFile(1, "/dev/stdout")
	os.Stderr = os.NewFile(2, "/dev/stderr")
}
