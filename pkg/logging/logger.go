// Package logging implements the slinit logging subsystem.
package logging

import (
	"fmt"
	"io"
	"log/syslog"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Level represents the logging level.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelNotice
	LevelWarn
	LevelError
)

// TimestampFormat selects how the console log line prefix is rendered.
type TimestampFormat int

const (
	// TimestampWallclock is the default: "15:04:05" local time.
	TimestampWallclock TimestampFormat = iota

	// TimestampISO8601 renders an ISO-8601 timestamp with millisecond
	// precision in the local timezone (e.g. 2026-04-17T10:31:04.213).
	TimestampISO8601

	// TimestampTAI64N renders the daemontools/s6-log external encoding:
	// '@' + 16 hex TAI seconds + 8 hex nanoseconds. Makes slinit logs
	// interchangeable with tai64nlocal(1).
	TimestampTAI64N

	// TimestampNone omits the prefix entirely (handy when piping into
	// another logger that prepends its own timestamp).
	TimestampNone
)

// ParseTimestampFormat accepts the CLI spelling of a TimestampFormat.
func ParseTimestampFormat(s string) (TimestampFormat, error) {
	switch s {
	case "", "wallclock", "time", "default":
		return TimestampWallclock, nil
	case "iso", "iso8601":
		return TimestampISO8601, nil
	case "tai64n", "tai":
		return TimestampTAI64N, nil
	case "none", "off":
		return TimestampNone, nil
	default:
		return TimestampWallclock, fmt.Errorf("invalid timestamp format %q (want wallclock|iso|tai64n|none)", s)
	}
}

// timestampFormat is the package-wide timestamp format used by Logger
// instances. Changed via SetTimestampFormat before any log lines are
// emitted; races on this global are best-effort, matching the rest of
// the package's configure-then-use pattern.
var timestampFormat = TimestampWallclock

// SetTimestampFormat changes the console log line timestamp encoding
// for all subsequent log lines.
func SetTimestampFormat(f TimestampFormat) { timestampFormat = f }

// formatTimestamp renders t according to the currently selected format.
// Returns an empty string for TimestampNone so callers can drop the
// entire "[...] " prefix.
func formatTimestamp(t time.Time) string {
	switch timestampFormat {
	case TimestampISO8601:
		return t.Format("2006-01-02T15:04:05.000")
	case TimestampTAI64N:
		// daemontools convention: TAI = 2^62 + unix_seconds + 10.
		// Leap seconds beyond the +10 base are not accounted for —
		// matches what tai64n(1) and s6-log 't' produce in practice.
		const tai64Offset = int64(1) << 62
		const taiLeapBase = int64(10)
		secs := uint64(tai64Offset + t.Unix() + taiLeapBase)
		nsecs := uint32(t.Nanosecond())
		return fmt.Sprintf("@%016x%08x", secs, nsecs)
	case TimestampNone:
		return ""
	default:
		return t.Format("15:04:05")
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelNotice:
		return "NOTICE"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

func (l Level) syslogPriority() syslog.Priority {
	switch l {
	case LevelDebug:
		return syslog.LOG_DEBUG
	case LevelInfo:
		return syslog.LOG_INFO
	case LevelNotice:
		return syslog.LOG_NOTICE
	case LevelWarn:
		return syslog.LOG_WARNING
	case LevelError:
		return syslog.LOG_ERR
	default:
		return syslog.LOG_CRIT
	}
}

// Logger provides structured logging for slinit.
type Logger struct {
	level     Level
	output    io.Writer
	syslogW   *syslog.Writer
	syslogQ   *syslogQueue
	mainLevel Level // minimum level for main log (syslog/file); defaults to same as level

	// consoleDup is an optional secondary writer that receives a copy of
	// every console-level log line. Used with --console-dup / -1 to tee
	// output to /dev/console even when --log-file redirects l.output to
	// a file. Inspired by s6-linux-init-maker's -1 flag.
	consoleDup io.Writer

	// bootConsole renders service start/stop/fail events as compact
	// "[ OK ] name" / "[FAIL] name" status lines on the console (the
	// production boot look) instead of the verbose timestamped stream.
	// The full detail is still recorded in the main log (syslog/file).
	bootConsole bool

	// bootConsolePaused, when non-zero, suppresses bootStatus writes
	// to the console (and consoleDup). Used by recovery.Debugger's
	// menu present-loop to keep the boxed menu from being trampled by
	// concurrent [ OK ] name lines from services that finish while
	// the operator reads the menu. Syslog/main log still records the
	// events — this only affects the compact console renderer.
	bootConsolePaused atomic.Bool

	// color enables ANSI color in the boot-console status markers.
	color bool

	// consoleIsTTY says whether the console can act on cursor control.
	// Only the clear-line sequence at the start of shutdown depends on
	// it: written into a pipe (a container's log stream) it is junk,
	// and it is not covered by NO_COLOR, which is about colour.
	// Defaults to true so a caller that never sets it behaves as before.
	consoleIsTTY bool

	// shuttingDown, when set, makes the boot console render service stop
	// events as "[STOPPD] name" (the teardown look) instead of "[ OK ] name".
	// Flipped on once when shutdown begins, via SetShutdownConsole.
	shuttingDown bool

	// ringBuf, when non-nil, receives a copy of every formatted log
	// line the Logger emits. Used by the runsvdir-inspired periodic
	// re-emitter to keep transient warnings visible.
	ringBuf *RingBuffer
}

// ANSI escape sequences for boot-console status markers.
const (
	ansiGreen = "\033[1;32m"
	ansiRed   = "\033[1;31m"
	ansiReset = "\033[0m"
)

// New creates a new Logger with the specified minimum level.
func New(level Level) *Logger {
	return &Logger{level: level, output: os.Stderr, mainLevel: level, consoleIsTTY: true}
}

// SetRingBuffer attaches a ring buffer that receives a copy of every
// formatted log line. Pass nil to detach. Safe to call at any time.
func (l *Logger) SetRingBuffer(rb *RingBuffer) {
	l.ringBuf = rb
}

// SetOutput redirects log output to the given writer.
func (l *Logger) SetOutput(w io.Writer) {
	l.output = w
}

// SetConsoleDup sets a secondary writer that receives a copy of every
// console-level log line. This is typically /dev/console, used when
// --log-file redirects the primary output to a file but the operator
// still wants to see logs on the physical console.
func (l *Logger) SetConsoleDup(w io.Writer) {
	l.consoleDup = w
}

// SetLevel changes the minimum logging level.
func (l *Logger) SetLevel(level Level) {
	l.level = level
	l.mainLevel = level
}

// SetMainLevel sets the minimum level for the main log (syslog/file) independently
// of the console level. This mirrors dinit's separate log-level / console-level.
func (l *Logger) SetMainLevel(level Level) {
	l.mainLevel = level
}

// SetBootConsole toggles the production boot console. When enabled, service
// start/stop/fail events render as compact "[ OK ] name" / "[FAIL] name"
// status lines on the console instead of the verbose timestamped stream;
// the full events are still written to the main log (syslog/file). color
// toggles ANSI coloring of the status marker.
// SetConsoleIsTTY records whether the console can act on cursor-control
// sequences. False suppresses the clear-line written at the start of
// shutdown, which is junk in a log stream.
func (l *Logger) SetConsoleIsTTY(v bool) { l.consoleIsTTY = v }

func (l *Logger) SetBootConsole(enabled, color bool) {
	l.bootConsole = enabled
	l.color = color
}

// SetShutdownConsole switches the boot console into teardown mode, where
// service stop events render as "[STOPPD] name" instead of "[ OK ] name".
// Called once when shutdown begins; harmless when the boot console is off.
//
// On the transition into shutdown mode we emit a cursor-reset + clear-line
// + newline sequence so the first [STOPPD] line starts on a fresh row,
// even if the console still shows a lingering getty "login:" prompt from
// tty1 (the two writers otherwise collide on the same visible row).
func (l *Logger) SetShutdownConsole(enabled bool) {
	if enabled && !l.shuttingDown && l.bootConsole && l.consoleIsTTY {
		const clearLine = "\r\x1b[2K\r\n"
		fmt.Fprint(l.output, clearLine)
		if l.consoleDup != nil {
			fmt.Fprint(l.consoleDup, clearLine)
		}
	}
	l.shuttingDown = enabled
}

// FinishBootConsole emits one blank line on the boot console at end of boot,
// so a getty's "login:" banner — which on a serial console (console=ttyS0,
// where /dev/console and the getty's tty are the same device) shares the
// line with slinit's "[ OK ]" status output — starts on its own row instead
// of landing on the last "[ OK ]" marker, whose tail the banner would
// otherwise overwrite. It is the boot-side companion of SetShutdownConsole,
// but writes a plain "\r\n" rather than a clear-line: at boot the getty may
// already have printed, and "\x1b[2K" would wipe its banner, whereas a
// newline only ever adds separation. Called once from OnBootReady, before
// the console tee is muted, so it passes through in order after the final
// "[ OK ]". No-op off a TTY (a container's log pipe) or when the boot
// console is disabled.
func (l *Logger) FinishBootConsole() {
	if !l.bootConsole || !l.consoleIsTTY {
		return
	}
	const nl = "\r\n"
	fmt.Fprint(l.output, nl)
	if l.consoleDup != nil {
		fmt.Fprint(l.consoleDup, nl)
	}
}

// bootStatus writes a "<marker> name" status line to the console (and the
// console-dup writer, if any). The marker comes from markerOK/markerFail/
// markerStopped, which apply ANSI color when l.color is set. Suppressed
// when bootConsolePaused is set (see PauseBootConsole) so an interactive
// menu on /dev/console isn't trampled by concurrent service transitions.
func (l *Logger) bootStatus(marker, name string) {
	if l.bootConsolePaused.Load() {
		return
	}
	line := fmt.Sprintf("%s %s\n", marker, name)
	l.writeConsole(line)
}

// writeConsole writes one console line to l.output and the console-dup
// writer, ending it with "\r\n" where the writer is a terminal.
//
// The kernel turns "\n" into "\r\n" only while the tty has OPOST and
// ONLCR set, and whatever holds the console's VT decides that. sudo does,
// by default since 1.9.14: with use_pty it runs the command on a pty of
// its own and puts the user's terminal in raw mode, OPOST cleared, for as
// long as the command runs (measured: "opost" outside sudo, "-opost"
// inside). So after `sudo su` and `reboot` on tty1, every shutdown status
// line on the VGA screen began where the one above it ended — a staircase
// — until sudo itself was stopped. systemd ends its status lines with
// "\r\n" on a terminal so as not to depend on the tty's settings
// (show-status.c); dinit ends them with "\n" and shows the same staircase. The dup writer is always a console device; l.output is a
// terminal unless consoleIsTTY says otherwise (a container's log pipe,
// where a "\r" would be junk).
func (l *Logger) writeConsole(line string) {
	ttyLine := strings.TrimSuffix(line, "\n") + "\r\n"
	if l.consoleIsTTY {
		fmt.Fprint(l.output, ttyLine)
	} else {
		fmt.Fprint(l.output, line)
	}
	if l.consoleDup != nil {
		fmt.Fprint(l.consoleDup, ttyLine)
	}
}

// PauseBootConsole silently suppresses every console write — the
// "[ OK ] name" compact renderer and ordinary log lines alike — while a
// caller holds /dev/console for an interactive menu (recovery.Debugger,
// PresentCollapse, Present).
//
// It covered only the compact renderer until 2.5.0, which was not enough:
// a WARN from the menu's own dispatch landed between two rows of the box
// it was drawing. Anything that writes to the console during a menu cuts
// that menu open, whatever level it came in at.
//
// Syslog, the ring buffer and the journal continue to record events —
// this is UI-only. Idempotent under the atomic; ResumeBootConsole is the
// inverse, and every caller arms it with defer so a menu cannot leave the
// console muted.
func (l *Logger) PauseBootConsole()  { l.bootConsolePaused.Store(true) }
func (l *Logger) ResumeBootConsole() { l.bootConsolePaused.Store(false) }

// markerOK renders the "[ OK ]" success marker, green when color is enabled.
func (l *Logger) markerOK() string {
	if l.color {
		return "[ " + ansiGreen + "OK" + ansiReset + " ]"
	}
	return "[ OK ]"
}

// markerFail renders the "[FAIL]" failure marker, red when color is enabled.
func (l *Logger) markerFail() string {
	if l.color {
		return "[" + ansiRed + "FAIL" + ansiReset + "]"
	}
	return "[FAIL]"
}

// markerStopped renders the "[STOPPD]" teardown marker shown for each service
// stopped during shutdown. Left uncolored to match the dinit teardown look.
func (l *Logger) markerStopped() string {
	return "[STOPPD]"
}

// mainLog records a message in the main log (syslog) only, bypassing the
// console. Used by the boot-console reporter, which prints its own compact
// status line to the console but still wants the full event in the main log.
func (l *Logger) mainLog(level Level, format string, args ...interface{}) {
	if l.syslogW == nil || level < l.mainLevel {
		return
	}
	l.logToSyslog(level, fmt.Sprintf(format, args...))
}

// newSyslog connects to the system log; a test points it elsewhere.
var newSyslog = func() (*syslog.Writer, error) {
	return syslog.New(syslog.LOG_DAEMON|syslog.LOG_NOTICE, "slinit")
}

// SetSyslog enables syslog output as the main log facility (like dinit's /dev/log).
// Messages are sent to the daemon facility. Returns an error if the syslog
// connection cannot be established; in that case the logger continues to work
// with console output only.
func (l *Logger) SetSyslog() error {
	w, err := newSyslog()
	if err != nil {
		return err
	}
	l.syslogW = w
	l.syslogQ = startSyslogQueue(w)
	return nil
}

// CloseSyslog closes the syslog connection if one is open, after passing
// on what is queued for it, for as long as the daemon takes it and at
// most syslogFlushMax.
func (l *Logger) CloseSyslog() {
	if l.syslogW != nil {
		if l.syslogQ.stop() {
			l.syslogW.Close()
		}
		l.syslogW = nil
		l.syslogQ = nil
	}
}

// syslogQueueLen bounds the messages waiting for the syslog daemon.
const syslogQueueLen = 1024

// syslogFlushStall is how long a message waits for room in the queue,
// and CloseSyslog for the queue to drain, while the daemon takes no
// message at all. A daemon that is reading takes one well within it.
const syslogFlushStall = time.Second

// syslogFlushMax bounds how long CloseSyslog waits for a slow daemon.
const syslogFlushMax = 2 * time.Second

type syslogMsg struct {
	level Level
	msg   string
}

// syslogQueue passes messages to the syslog daemon from a goroutine of its
// own. log/syslog writes with no deadline, and slinit logs with the
// scheduling lock held, so a daemon that stopped reading /dev/log (one on
// the host, in container mode; or slinit not run as PID 1) stopped slinit
// at its next message. When the queue is full a message waits for room
// while the daemon keeps taking messages, as a direct write did; it is
// dropped only once the daemon has taken none for syslogFlushStall, and
// after that messages are dropped without waiting until it takes one
// again. The daemon is told how many were dropped when it reads again. A
// daemon that takes its messages, however slowly, gets the same ones, in
// the same order.
type syslogQueue struct {
	w       *syslog.Writer
	q       chan syslogMsg
	quit    chan struct{}
	done    chan struct{}
	sent    atomic.Uint64
	dropped atomic.Uint64

	// stalled is set when a message gave up waiting for room; stalledAt
	// is sent at that moment, so the first message the daemon takes
	// after it clears it.
	stalled   atomic.Bool
	stalledAt atomic.Uint64
}

func startSyslogQueue(w *syslog.Writer) *syslogQueue {
	sq := &syslogQueue{
		w:    w,
		q:    make(chan syslogMsg, syslogQueueLen),
		quit: make(chan struct{}),
		done: make(chan struct{}),
	}
	go sq.run()
	return sq
}

func (sq *syslogQueue) put(level Level, msg string) {
	m := syslogMsg{level, msg}
	select {
	case sq.q <- m:
		return
	default:
	}
	if sq.stalled.Load() {
		if sq.sent.Load() == sq.stalledAt.Load() {
			sq.dropped.Add(1)
			return
		}
		sq.stalled.Store(false)
	}
	timer := time.NewTimer(syslogFlushStall)
	defer timer.Stop()
	for {
		last := sq.sent.Load()
		select {
		case sq.q <- m:
			return
		case <-timer.C:
		}
		if now := sq.sent.Load(); now == last {
			sq.stalledAt.Store(now)
			sq.stalled.Store(true)
			sq.dropped.Add(1)
			return
		}
		timer.Reset(syslogFlushStall)
	}
}

func (sq *syslogQueue) run() {
	defer close(sq.done)
	for {
		select {
		case m := <-sq.q:
			sq.write(m)
		case <-sq.quit:
			for {
				select {
				case m := <-sq.q:
					sq.write(m)
				default:
					return
				}
			}
		}
	}
}

func (sq *syslogQueue) write(m syslogMsg) {
	if n := sq.dropped.Swap(0); n > 0 {
		sq.w.Warning(fmt.Sprintf("%d messages to syslog were dropped while it was not reading", n))
	}
	writeSyslog(sq.w, m.level, m.msg)
	sq.sent.Add(1)
}

// stop lets the queue drain while the daemon keeps taking messages, for
// at most syslogFlushMax, and reports whether it finished. A daemon that
// has stopped, or is too slow, is left behind with the goroutine, which
// still holds the writer.
func (sq *syslogQueue) stop() bool {
	close(sq.quit)
	last := sq.sent.Load()
	end := time.Now().Add(syslogFlushMax)
	for {
		wait := min(syslogFlushStall, time.Until(end))
		if wait <= 0 {
			return false
		}
		select {
		case <-sq.done:
			return true
		case <-time.After(wait):
		}
		n := sq.sent.Load()
		if n == last {
			return false
		}
		last = n
	}
}

func (l *Logger) log(level Level, format string, args ...interface{}) {
	consoleOK := level >= l.level
	syslogOK := l.syslogW != nil && level >= l.mainLevel
	if !consoleOK && !syslogOK {
		return
	}

	msg := fmt.Sprintf(format, args...)

	if consoleOK {
		timestamp := formatTimestamp(time.Now())
		var line string
		if timestamp == "" {
			line = fmt.Sprintf("%s: %s\n", level, msg)
		} else {
			line = fmt.Sprintf("[%s] %s: %s\n", timestamp, level, msg)
		}
		// An interactive menu on /dev/console owns the screen while it
		// is up. Writing a log line into it cuts the menu's box open —
		// observed with a WARN landing between two action rows of the
		// boot debugger. Only the compact "[ OK ] name" renderer used to
		// respect the pause, which left every other level free to
		// scribble over the one screen an operator reads when something
		// has already gone wrong.
		//
		// The record is not lost: syslog below, the ring buffer here,
		// and the journal event bus are all outside this gate. Only the
		// console write waits for the menu to let go.
		if !l.bootConsolePaused.Load() {
			l.writeConsole(line)
		}
		if l.ringBuf != nil {
			// Ring buffer capture is best-effort — it returns nil
			// unconditionally, so the error return is elided.
			_, _ = l.ringBuf.Write([]byte(line))
		}
	}

	if syslogOK {
		l.logToSyslog(level, msg)
	}
}

func (l *Logger) logToSyslog(level Level, msg string) {
	if q := l.syslogQ; q != nil {
		q.put(level, msg)
	}
}

func writeSyslog(w *syslog.Writer, level Level, msg string) {
	switch level {
	case LevelDebug:
		w.Debug(msg)
	case LevelInfo:
		w.Info(msg)
	case LevelNotice:
		w.Notice(msg)
	case LevelWarn:
		w.Warning(msg)
	case LevelError:
		w.Err(msg)
	default:
		w.Crit(msg)
	}
}

// Debug logs at debug level.
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(LevelDebug, format, args...)
}

// Info logs at info level.
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(LevelInfo, format, args...)
}

// Notice logs at notice level.
func (l *Logger) Notice(format string, args ...interface{}) {
	l.log(LevelNotice, format, args...)
}

// Warn logs at warn level.
func (l *Logger) Warn(format string, args ...interface{}) {
	l.log(LevelWarn, format, args...)
}

// Error logs at error level.
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(LevelError, format, args...)
}

// ServiceStarted logs a service start event.
func (l *Logger) ServiceStarted(name string) {
	if l.bootConsole {
		l.bootStatus(l.markerOK(), name)
		l.mainLog(LevelInfo, "Service '%s' started", name)
		return
	}
	l.log(LevelInfo, "Service '%s' started", name)
}

// ServiceStopped logs a service stop event. The boot console renders it as
// "[STOPPD] name".
//
// It used to render as "[ OK ] name" outside shutdown, which read as
// success for a service that had just died: a crashed process produced
// an ERROR line about its exit code followed by "[ OK ] name". "Stopped"
// is what happened either way; whether it was wanted is the business of
// the surrounding log lines.
func (l *Logger) ServiceStopped(name string) {
	if l.bootConsole {
		l.bootStatus(l.markerStopped(), name)
		l.mainLog(LevelInfo, "Service '%s' stopped", name)
		return
	}
	l.log(LevelInfo, "Service '%s' stopped", name)
}

// ServiceFailed logs a service failure event.
func (l *Logger) ServiceFailed(name string, depFailed bool) {
	if l.bootConsole {
		l.bootStatus(l.markerFail(), name)
		if depFailed {
			l.mainLog(LevelError, "Service '%s' failed to start (dependency failed)", name)
		} else {
			l.mainLog(LevelError, "Service '%s' failed to start", name)
		}
		return
	}
	if depFailed {
		l.log(LevelError, "Service '%s' failed to start (dependency failed)", name)
	} else {
		l.log(LevelError, "Service '%s' failed to start", name)
	}
}
