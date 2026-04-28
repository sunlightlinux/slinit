package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LogRotator manages logfile writing with rotation, filtering, and post-rotate processing.
// Inspired by runit's svlogd but using dinit-compatible config keys.
type LogRotator struct {
	mu sync.Mutex

	// Config
	filePath  string
	filePerms os.FileMode
	fileUID   int
	fileGID   int
	maxSize   int64         // rotate when file exceeds this size (0 = no size limit)
	maxFiles  int           // keep at most this many rotated files (0 = unlimited)
	rotateInt time.Duration // rotate at this interval (0 = no time rotation)
	processor []string      // command to run on rotated file
	includes  []*regexp.Regexp
	excludes  []*regexp.Regexp

	// State
	file        *os.File
	currentSize int64
	lastRotate  time.Time
	rotateTimer *time.Timer
	pipeR       *os.File
	pipeW       *os.File
	doneCh      chan struct{}
	running     bool
	serviceName string
	logger      interface{ Info(string, ...interface{}); Error(string, ...interface{}) }
}

// LogRotatorConfig holds configuration for a LogRotator.
type LogRotatorConfig struct {
	FilePath    string
	FilePerms   os.FileMode
	FileUID     int
	FileGID     int
	MaxSize     int64
	MaxFiles    int
	RotateTime  time.Duration
	Processor   []string
	Includes    []string
	Excludes    []string
	ServiceName string
	Logger      interface{ Info(string, ...interface{}); Error(string, ...interface{}) }
}

// NewLogRotator creates a new LogRotator with the given configuration.
func NewLogRotator(cfg LogRotatorConfig) (*LogRotator, error) {
	lr := &LogRotator{
		filePath:    cfg.FilePath,
		filePerms:   cfg.FilePerms,
		fileUID:     cfg.FileUID,
		fileGID:     cfg.FileGID,
		maxSize:     cfg.MaxSize,
		maxFiles:    cfg.MaxFiles,
		rotateInt:   cfg.RotateTime,
		processor:   cfg.Processor,
		serviceName: cfg.ServiceName,
		logger:      cfg.Logger,
		lastRotate:  time.Now(),
	}
	if lr.filePerms == 0 {
		lr.filePerms = 0600
	}

	// Compile include/exclude patterns
	for _, pat := range cfg.Includes {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid log-include pattern '%s': %w", pat, err)
		}
		lr.includes = append(lr.includes, re)
	}
	for _, pat := range cfg.Excludes {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("invalid log-exclude pattern '%s': %w", pat, err)
		}
		lr.excludes = append(lr.excludes, re)
	}

	return lr, nil
}

// CreatePipe creates a pipe and returns the write end for the child process.
func (lr *LogRotator) CreatePipe() (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	lr.pipeR = r
	lr.pipeW = w
	return w, nil
}

// CloseWriteEnd closes the parent's copy of the write end after fork.
func (lr *LogRotator) CloseWriteEnd() {
	if lr.pipeW != nil {
		lr.pipeW.Close()
		lr.pipeW = nil
	}
}

// StartReader starts the goroutine that reads from the pipe and writes to the logfile.
func (lr *LogRotator) StartReader() {
	if lr.pipeR == nil {
		return
	}
	lr.mu.Lock()
	if lr.running {
		lr.mu.Unlock()
		return
	}
	lr.doneCh = make(chan struct{})
	lr.running = true
	pipeR := lr.pipeR
	doneCh := lr.doneCh
	lr.mu.Unlock()

	// Start time-based rotation timer
	if lr.rotateInt > 0 {
		lr.rotateTimer = time.AfterFunc(lr.rotateInt, func() {
			lr.mu.Lock()
			defer lr.mu.Unlock()
			lr.rotateLocked()
			if lr.rotateTimer != nil {
				lr.rotateTimer.Reset(lr.rotateInt)
			}
		})
	}

	go lr.readLoop(pipeR, doneCh)
}

// readLoop reads lines from pipeR, filters them, and writes to the logfile.
func (lr *LogRotator) readLoop(pipeR *os.File, doneCh chan struct{}) {
	defer func() {
		pipeR.Close()
		lr.mu.Lock()
		if lr.file != nil {
			lr.file.Close()
			lr.file = nil
		}
		if lr.rotateTimer != nil {
			lr.rotateTimer.Stop()
			lr.rotateTimer = nil
		}
		lr.running = false
		lr.mu.Unlock()
		close(doneCh)
	}()

	buf := make([]byte, 4096)
	var lineBuf []byte

	for {
		n, err := pipeR.Read(buf)
		if n > 0 {
			// Process data line by line for filtering
			data := buf[:n]
			for len(data) > 0 {
				idx := bytes.IndexByte(data, '\n')
				if idx >= 0 {
					lineBuf = append(lineBuf, data[:idx+1]...)
					lr.processLine(lineBuf)
					lineBuf = lineBuf[:0]
					data = data[idx+1:]
				} else {
					lineBuf = append(lineBuf, data...)
					data = nil
				}
			}
		}
		if err != nil {
			// Flush remaining partial line
			if len(lineBuf) > 0 {
				lr.processLine(lineBuf)
			}
			return
		}
	}
}

// processLine filters and writes a single line to the logfile.
// Uses regexp.Match on raw bytes to avoid string conversion allocations.
func (lr *LogRotator) processLine(line []byte) {
	if len(line) == 0 {
		return
	}

	// Trim trailing newline for matching (without allocation)
	matchLine := line
	if matchLine[len(matchLine)-1] == '\n' {
		matchLine = matchLine[:len(matchLine)-1]
	}

	// Apply include filters (if any, line must match at least one)
	if len(lr.includes) > 0 {
		matched := false
		for _, re := range lr.includes {
			if re.Match(matchLine) {
				matched = true
				break
			}
		}
		if !matched {
			return
		}
	}

	// Apply exclude filters (if any match, skip the line)
	for _, re := range lr.excludes {
		if re.Match(matchLine) {
			return
		}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	// Open file if needed
	if lr.file == nil {
		if err := lr.openFileLocked(); err != nil {
			return
		}
	}

	// Check size-based rotation
	if lr.maxSize > 0 && lr.currentSize+int64(len(line)) > lr.maxSize {
		lr.rotateLocked()
	}

	n, err := lr.file.Write(line)
	if err == nil {
		lr.currentSize += int64(n)
	}
}

// openFileLocked opens the logfile for appending. Must be called with mu held.
//
// O_NOFOLLOW prevents an attacker (or a buggy service writing to a shared
// /var/log path) from replacing the logfile with a symlink to /etc/passwd
// or similar — slinit runs as root so a followed symlink would be an
// arbitrary write primitive. fchown on the open fd (instead of path-based
// os.Chown) closes the same TOCTOU window for the chown step.
func (lr *LogRotator) openFileLocked() error {
	f, err := os.OpenFile(lr.filePath, os.O_WRONLY|os.O_CREATE|os.O_APPEND|syscall.O_NOFOLLOW, lr.filePerms)
	if err != nil {
		return err
	}
	if lr.fileUID >= 0 || lr.fileGID >= 0 {
		_ = f.Chown(lr.fileUID, lr.fileGID)
	}
	// Get current file size
	info, err := f.Stat()
	if err == nil {
		lr.currentSize = info.Size()
	}
	lr.file = f
	return nil
}

// rotateLocked rotates the logfile. Must be called with mu held.
func (lr *LogRotator) rotateLocked() {
	if lr.file == nil {
		return
	}
	lr.file.Close()
	lr.file = nil
	lr.currentSize = 0
	lr.lastRotate = time.Now()

	// Rename current to timestamped file
	rotatedName := fmt.Sprintf("%s.%s", lr.filePath, lr.lastRotate.Format("20060102-150405"))
	if err := os.Rename(lr.filePath, rotatedName); err != nil {
		if lr.logger != nil {
			lr.logger.Error("Service '%s': logfile rotate rename failed: %v", lr.serviceName, err)
		}
		return
	}

	// Clean up old rotated files
	if lr.maxFiles > 0 {
		lr.cleanOldFilesLocked()
	}

	// Run log processor on rotated file
	if len(lr.processor) > 0 {
		go lr.runProcessor(rotatedName)
	}

	// Reopen current logfile
	lr.openFileLocked()
}

// cleanOldFilesLocked removes rotated files exceeding maxFiles. Must be called with mu held.
func (lr *LogRotator) cleanOldFilesLocked() {
	dir := filepath.Dir(lr.filePath)
	base := filepath.Base(lr.filePath)
	prefix := base + "."

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var rotated []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) && e.Name() != base {
			rotated = append(rotated, e.Name())
		}
	}

	if len(rotated) <= lr.maxFiles {
		return
	}

	// Sort ascending (oldest first)
	sort.Strings(rotated)

	// Remove oldest files
	toRemove := len(rotated) - lr.maxFiles
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(dir, rotated[i])
		os.Remove(path)
	}
}

// runProcessor runs the log processor command on a rotated file.
func (lr *LogRotator) runProcessor(rotatedFile string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := make([]string, len(lr.processor)-1, len(lr.processor))
	copy(args, lr.processor[1:])
	args = append(args, rotatedFile)

	cmd := exec.CommandContext(ctx, lr.processor[0], args...)
	if lr.logger != nil {
		lr.logger.Info("Service '%s': running log-processor on %s", lr.serviceName, rotatedFile)
	}
	if err := cmd.Run(); err != nil {
		if lr.logger != nil {
			lr.logger.Error("Service '%s': log-processor failed: %v", lr.serviceName, err)
		}
	}
}

// Close stops the reader and cleans up resources.
func (lr *LogRotator) Close() {
	if lr.pipeW != nil {
		lr.pipeW.Close()
		lr.pipeW = nil
	}
	if lr.pipeR != nil {
		lr.pipeR.Close()
		lr.pipeR = nil
	}
	lr.mu.Lock()
	running := lr.running
	doneCh := lr.doneCh
	lr.mu.Unlock()
	if doneCh != nil && running {
		<-doneCh
	}
}
