package service

import (
	"bytes"
	"context"
	"errors"
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
	minFiles  int           // svlogd Nmin: drain rotated files down to this count on ENOSPC (0 = disabled)
	rotateInt time.Duration // rotate at this interval (0 = no time rotation)
	processor []string      // command to run on rotated file
	includes  []*regexp.Regexp
	excludes  []*regexp.Regexp

	// Rate limiter (token bucket). rateInterval==0 means disabled.
	rateInterval     time.Duration
	rateBurst        int
	rateTokens       float64   // current available tokens
	rateLastRefill   time.Time // last time we refilled the bucket
	rateDropped      uint64    // lines dropped since last reset
	rateDropReported bool      // emitted the "rate limit hit" notice already

	// Severity filter. levelMax == -1 means disabled. 0..7 follow
	// syslog levels: 0=emerg (most severe) .. 7=debug (least).
	levelMax int

	// svlogd -r / -R sanitization. When sanitizeChar != 0, control
	// bytes (< 0x20, plus 0x7F) other than LF are replaced with
	// sanitizeChar. sanitizeExtra[b] flags additional bytes to
	// replace — populated from the log-sanitize-extra config.
	sanitizeChar  byte
	sanitizeExtra [256]bool

	// svlogd -l: max line length in bytes. 0 disables. When active,
	// content longer than maxLineLen is truncated to maxLineLen bytes
	// then marked with '+' before the newline. A no-newline overflow
	// also flips readLoop into discard-until-'\n' mode so unbounded
	// input can't balloon lineBuf.
	maxLineLen int

	// svlogd -t/-tt/-ttt: timestamp mode prepended to every line.
	// "" = disabled. Others: "tai64n", "human", "iso8601".
	tsMode string
	// svlogd log/config p<prefix>: static per-line prefix, emitted
	// after the timestamp (if any). Includes the trailing space.
	linePrefix []byte

	// State
	file            *os.File
	currentSize     int64
	lastRotate      time.Time
	rotateTimer     *time.Timer
	enospcReported  bool // one-shot: we already logged the ENOSPC drain event
	pipeR       *os.File
	pipeW       *os.File
	doneCh      chan struct{}
	running     bool
	serviceName string
	logger      interface {
		Info(string, ...interface{})
		Error(string, ...interface{})
	}
}

// LogRotatorConfig holds configuration for a LogRotator.
type LogRotatorConfig struct {
	FilePath    string
	FilePerms   os.FileMode
	FileUID     int
	FileGID     int
	MaxSize     int64
	MaxFiles    int
	MinFiles    int // svlogd Nmin: floor for ENOSPC drain (0 = disabled)
	RotateTime  time.Duration
	Processor   []string
	Includes    []string
	Excludes    []string
	ServiceName string
	// Rate limit: drop lines exceeding RateBurst per RateInterval.
	// Both must be > 0 for the limiter to engage.
	RateInterval time.Duration
	RateBurst    int
	// Severity filter: 0..7 (syslog), -1 to disable.
	LogLevelMax int
	// svlogd -r: replacement byte for control chars (0 = disabled).
	SanitizeChar byte
	// svlogd -R: bytes to additionally sanitize (each becomes SanitizeChar).
	// If SanitizeExtra is non-empty and SanitizeChar is 0, the default
	// replacement '_' is used.
	SanitizeExtra []byte
	// svlogd -l: hard cap on line length in bytes (0 = disabled).
	MaxLineLength int
	// svlogd -t/-tt/-ttt: line timestamp mode. Accepts "tai64n",
	// "human", "iso8601", or "" (disabled).
	TimestampMode string
	// svlogd log/config p<prefix>: static per-line prefix. Emitted
	// after any timestamp and before the line content. A trailing
	// space is added automatically at load time if omitted.
	LinePrefix string
	Logger      interface {
		Info(string, ...interface{})
		Error(string, ...interface{})
	}
}

// NewLogRotator creates a new LogRotator with the given configuration.
func NewLogRotator(cfg LogRotatorConfig) (*LogRotator, error) {
	lr := &LogRotator{
		filePath:     cfg.FilePath,
		filePerms:    cfg.FilePerms,
		fileUID:      cfg.FileUID,
		fileGID:      cfg.FileGID,
		maxSize:      cfg.MaxSize,
		maxFiles:     cfg.MaxFiles,
		minFiles:     cfg.MinFiles,
		rotateInt:    cfg.RotateTime,
		processor:    cfg.Processor,
		serviceName:  cfg.ServiceName,
		logger:       cfg.Logger,
		lastRotate:   time.Now(),
		rateInterval: cfg.RateInterval,
		rateBurst:    cfg.RateBurst,
		levelMax:     cfg.LogLevelMax,
	}
	if lr.levelMax == 0 && cfg.LogLevelMax == 0 {
		// Zero means "disabled" externally; internally we store -1 so
		// 0 (emerg) remains a usable threshold for callers that
		// explicitly request it via SetLogLevelMax.
		lr.levelMax = -1
	}
	if cfg.RateInterval > 0 && cfg.RateBurst > 0 {
		lr.rateTokens = float64(cfg.RateBurst)
		lr.rateLastRefill = time.Now()
	}
	if cfg.SanitizeChar != 0 || len(cfg.SanitizeExtra) > 0 {
		lr.sanitizeChar = cfg.SanitizeChar
		if lr.sanitizeChar == 0 {
			lr.sanitizeChar = '_'
		}
		for _, b := range cfg.SanitizeExtra {
			lr.sanitizeExtra[b] = true
		}
	}
	if cfg.MaxLineLength > 0 {
		lr.maxLineLen = cfg.MaxLineLength
	}
	if cfg.TimestampMode != "" {
		lr.tsMode = cfg.TimestampMode
	}
	if cfg.LinePrefix != "" {
		p := cfg.LinePrefix
		if !strings.HasSuffix(p, " ") {
			p += " "
		}
		lr.linePrefix = []byte(p)
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
	// discarding == true means we already emitted a truncated line
	// for the current input line and are now dropping bytes until we
	// see the terminating '\n'. Guards against a runaway producer
	// ballooning lineBuf beyond maxLineLen.
	discarding := false

	for {
		n, err := pipeR.Read(buf)
		if n > 0 {
			data := buf[:n]
			for len(data) > 0 {
				if discarding {
					idx := bytes.IndexByte(data, '\n')
					if idx < 0 {
						data = nil // whole chunk was mid-overflow
						continue
					}
					// Newline found — recover and move on.
					discarding = false
					data = data[idx+1:]
					continue
				}

				idx := bytes.IndexByte(data, '\n')
				if idx >= 0 {
					lineBuf = append(lineBuf, data[:idx+1]...)
					lr.processLine(lr.capLine(lineBuf))
					lineBuf = lineBuf[:0]
					data = data[idx+1:]
					continue
				}

				// No newline in this chunk — append and check overflow.
				lineBuf = append(lineBuf, data...)
				data = nil
				if lr.maxLineLen > 0 && len(lineBuf) > lr.maxLineLen {
					lr.processLine(lr.capLine(lineBuf))
					lineBuf = lineBuf[:0]
					discarding = true
				}
			}
		}
		if err != nil {
			// Flush remaining partial line (unless we're in discard mode
			// and the producer died without ever emitting '\n').
			if !discarding && len(lineBuf) > 0 {
				lr.processLine(lr.capLine(lineBuf))
			}
			return
		}
	}
}

// capLine implements the svlogd -l truncation semantic. Lines whose
// content (not counting the trailing '\n') is <= maxLineLen are
// returned unchanged. Longer lines are truncated to the first
// maxLineLen bytes and marked with a '+' before the newline so the
// operator can tell at a glance the line was clipped. When called
// during a mid-line overflow (no trailing '\n' in the buffer yet) we
// still emit a well-formed line with '+\n' at the end.
func (lr *LogRotator) capLine(line []byte) []byte {
	if lr.maxLineLen <= 0 {
		return line
	}
	hasNL := len(line) > 0 && line[len(line)-1] == '\n'
	content := line
	if hasNL {
		content = line[:len(line)-1]
	}
	if len(content) <= lr.maxLineLen {
		return line
	}
	out := make([]byte, 0, lr.maxLineLen+2)
	out = append(out, content[:lr.maxLineLen]...)
	out = append(out, '+', '\n')
	return out
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

	// Severity gate: drop lines with a syslog priority above the
	// configured maximum. Lines without a <N> prefix are treated as
	// "info" (6), so plain text output passes any threshold >= 6.
	if lr.levelMax >= 0 {
		if extractSyslogLevel(matchLine) > lr.levelMax {
			return
		}
	}

	// Rate limiter (token bucket). When the bucket is empty the line
	// is dropped and a single "rate limit hit" notice is emitted.
	if lr.rateInterval > 0 && lr.rateBurst > 0 {
		if !lr.tryConsumeRateToken() {
			lr.rateDropped++
			if !lr.rateDropReported && lr.logger != nil {
				lr.logger.Info(
					"Service '%s': log-rate-limit hit (%d bursts/%s); dropping further lines",
					lr.serviceName, lr.rateBurst, lr.rateInterval)
				lr.rateDropReported = true
			}
			return
		}
	}

	// svlogd -r/-R: replace unwanted bytes before the write. We mutate
	// `line` in place — readLoop hands us a slice from its own buffer
	// that is discarded after this call, so the mutation is not visible
	// elsewhere and no copy is needed.
	if lr.sanitizeChar != 0 {
		lr.sanitizeInPlace(line)
	}

	// svlogd -t/-tt/-ttt + p<prefix>: assemble the outbound record as
	// [timestamp] [prefix] content. We only allocate when at least one
	// of the two is configured; the plain-line hot path stays zero-copy.
	out := line
	if lr.tsMode != "" || len(lr.linePrefix) > 0 {
		out = lr.decorateLine(line)
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
	if lr.maxSize > 0 && lr.currentSize+int64(len(out)) > lr.maxSize {
		lr.rotateLocked()
	}

	n, err := lr.file.Write(out)
	if err == nil {
		lr.currentSize += int64(n)
		return
	}

	// svlogd Nmin analogue: if the filesystem is full and we have
	// rotated files we can sacrifice, drain oldest ones down to
	// minFiles and retry the write once. Better a shorter log history
	// than a lost stream of live events. We deliberately do NOT loop
	// on retries — a persistent ENOSPC after the drain means the
	// current-file writes themselves are the problem (large lines,
	// tiny partition), which retry cannot fix.
	if lr.minFiles > 0 && errors.Is(err, syscall.ENOSPC) {
		if lr.freeSpaceLocked() {
			if n2, err2 := lr.file.Write(out); err2 == nil {
				lr.currentSize += int64(n2)
			}
		}
	}
}

// decorateLine returns a new buffer of the form:
//
//	[timestamp ][prefix ]content\n
//
// with the trailing '\n' preserved when line already had one. The
// helper is only called when at least one of tsMode / linePrefix is
// configured, so it's fine to allocate a fresh slice each call.
func (lr *LogRotator) decorateLine(line []byte) []byte {
	hasNL := len(line) > 0 && line[len(line)-1] == '\n'
	content := line
	if hasNL {
		content = line[:len(line)-1]
	}

	ts := lr.formatTimestamp()
	// Reserve upper bound; ts + space + prefix + content + \n.
	out := make([]byte, 0, len(ts)+len(lr.linePrefix)+len(content)+2)
	if len(ts) > 0 {
		out = append(out, ts...)
		out = append(out, ' ')
	}
	if len(lr.linePrefix) > 0 {
		out = append(out, lr.linePrefix...)
	}
	out = append(out, content...)
	out = append(out, '\n')
	return out
}

// formatTimestamp returns the mode-appropriate timestamp bytes, or a
// nil/empty slice when timestamping is off. The three modes mirror
// svlogd -t / -tt / -ttt: an external tai64n-format token; a
// human-readable UTC form; and a strict ISO 8601 UTC form.
func (lr *LogRotator) formatTimestamp() []byte {
	now := time.Now().UTC()
	switch lr.tsMode {
	case "tai64n":
		// tai64n bytes: '@' + 16 hex digits (seconds w/ TAI epoch
		// offset 2^62 + 10 leap seconds), then 8 hex digits of nanos.
		// The leap-second table is baked into the offset constant so
		// the result matches daemontools' tai64n for a monotonic UTC
		// read on modern kernels — good enough for log stitching, not
		// suitable for high-precision timekeeping research.
		const tai64nEpoch = uint64(0x4000000000000000) + 10
		secs := tai64nEpoch + uint64(now.Unix())
		nanos := uint32(now.Nanosecond())
		buf := make([]byte, 0, 1+16+8)
		buf = append(buf, '@')
		buf = fmt.Appendf(buf, "%016x%08x", secs, nanos)
		return buf
	case "human":
		// YYYY-MM-DD_HH:MM:SS.µs — svlogd -tt style.
		return []byte(now.Format("2006-01-02_15:04:05.000000"))
	case "iso8601":
		// YYYY-MM-DDTHH:MM:SS.µsZ — sortable, strict-parse friendly.
		return []byte(now.Format("2006-01-02T15:04:05.000000Z"))
	}
	return nil
}

// sanitizeInPlace replaces control bytes (< 0x20, plus 0x7F DEL) and
// bytes flagged in sanitizeExtra with sanitizeChar. LF (0x0A) is always
// preserved so the log stays line-oriented; TAB (0x09) is preserved
// because operators expect indentation to survive (svlogd itself
// treats \t as a control char, but that trips up too many log formats
// so we diverge here — see log-sanitize-extra to add \t back).
func (lr *LogRotator) sanitizeInPlace(buf []byte) {
	for i, b := range buf {
		if b == '\n' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7F || lr.sanitizeExtra[b] {
			buf[i] = lr.sanitizeChar
		}
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
	// Fresh rotation cycle → arm the one-shot ENOSPC warning for the
	// next drain event, so an operator sees the disk-pressure signal
	// again if the newly-created file fills up.
	lr.enospcReported = false

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

// freeSpaceLocked implements svlogd's Nmin ENOSPC recovery. When the
// current write hits a full filesystem, we aggressively delete rotated
// files (oldest first) until only minFiles remain. Returns true if at
// least one file was removed, so the caller can meaningfully retry
// the write. A one-shot warning is emitted per drain event so the
// operator sees the disk-pressure signal in the daemon log without
// getting a flood of the same message on subsequent lines. Must be
// called with mu held.
func (lr *LogRotator) freeSpaceLocked() bool {
	dir := filepath.Dir(lr.filePath)
	base := filepath.Base(lr.filePath)
	prefix := base + "."

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
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

	if len(rotated) <= lr.minFiles {
		// Nothing to reclaim — either no rotated files or already at
		// the floor. The caller will not retry; the write is lost.
		return false
	}

	// Oldest first (filenames start with a sortable timestamp).
	sort.Strings(rotated)
	toRemove := len(rotated) - lr.minFiles

	removed := 0
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(dir, rotated[i])
		if err := os.Remove(path); err == nil {
			removed++
		}
	}

	if removed > 0 && !lr.enospcReported && lr.logger != nil {
		lr.logger.Error(
			"Service '%s': ENOSPC on logfile; drained %d rotated file(s) toward minimum %d",
			lr.serviceName, removed, lr.minFiles)
		lr.enospcReported = true
	}
	return removed > 0
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

// tryConsumeRateToken refills the token bucket based on elapsed time
// and consumes one token. Returns false when the bucket is empty.
// Refill rate is rateBurst tokens per rateInterval, capped at rateBurst.
func (lr *LogRotator) tryConsumeRateToken() bool {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	now := time.Now()
	if lr.rateLastRefill.IsZero() {
		lr.rateLastRefill = now
		lr.rateTokens = float64(lr.rateBurst)
	} else {
		elapsed := now.Sub(lr.rateLastRefill)
		if elapsed > 0 {
			refill := float64(lr.rateBurst) * elapsed.Seconds() / lr.rateInterval.Seconds()
			lr.rateTokens += refill
			if lr.rateTokens > float64(lr.rateBurst) {
				lr.rateTokens = float64(lr.rateBurst)
			}
			lr.rateLastRefill = now
		}
	}
	if lr.rateTokens >= 1 {
		lr.rateTokens -= 1
		// Reset the "dropped" notice once we have headroom again.
		lr.rateDropReported = false
		return true
	}
	return false
}

// extractSyslogLevel parses a leading <N> syslog priority prefix and
// returns the level component (0..7). Lines without a prefix are
// treated as info (6) so plain text output passes any threshold >= 6.
//
// Priority is `facility*8 + level`. We strip facility by masking with 7.
func extractSyslogLevel(line []byte) int {
	if len(line) < 3 || line[0] != '<' {
		return 6
	}
	end := -1
	for i := 1; i < len(line) && i <= 4; i++ {
		if line[i] == '>' {
			end = i
			break
		}
		if line[i] < '0' || line[i] > '9' {
			return 6
		}
	}
	if end < 2 {
		return 6
	}
	n := 0
	for i := 1; i < end; i++ {
		n = n*10 + int(line[i]-'0')
	}
	return n & 7
}

// ParseLogLevel decodes a level keyword used by log-level-max. Returns
// -1 for empty / "off" / "none" / "any" (disabled), or 0..7 for valid
// syslog severities. Unknown keywords yield an error.
func ParseLogLevel(s string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "off", "none", "any":
		return -1, nil
	case "0", "emerg", "panic":
		return 0, nil
	case "1", "alert":
		return 1, nil
	case "2", "crit", "critical":
		return 2, nil
	case "3", "err", "error":
		return 3, nil
	case "4", "warn", "warning":
		return 4, nil
	case "5", "notice":
		return 5, nil
	case "6", "info":
		return 6, nil
	case "7", "debug":
		return 7, nil
	}
	return -1, fmt.Errorf("unknown log level %q (use emerg/alert/crit/err/warn/notice/info/debug or 0..7)", s)
}
