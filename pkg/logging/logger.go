// Package logging implements the slinit logging subsystem.
package logging

import (
	"fmt"
	"io"
	"log/syslog"
	"os"
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
	level    Level
	output   io.Writer
	syslogW  *syslog.Writer
	mainLevel Level // minimum level for main log (syslog/file); defaults to same as level

	// consoleDup is an optional secondary writer that receives a copy of
	// every console-level log line. Used with --console-dup / -1 to tee
	// output to /dev/console even when --log-file redirects l.output to
	// a file. Inspired by s6-linux-init-maker's -1 flag.
	consoleDup io.Writer
}

// New creates a new Logger with the specified minimum level.
func New(level Level) *Logger {
	return &Logger{level: level, output: os.Stderr, mainLevel: level}
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

// SetSyslog enables syslog output as the main log facility (like dinit's /dev/log).
// Messages are sent to the daemon facility. Returns an error if the syslog
// connection cannot be established; in that case the logger continues to work
// with console output only.
func (l *Logger) SetSyslog() error {
	w, err := syslog.New(syslog.LOG_DAEMON|syslog.LOG_NOTICE, "slinit")
	if err != nil {
		return err
	}
	l.syslogW = w
	return nil
}

// CloseSyslog closes the syslog connection if one is open.
func (l *Logger) CloseSyslog() {
	if l.syslogW != nil {
		l.syslogW.Close()
		l.syslogW = nil
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
		fmt.Fprint(l.output, line)
		if l.consoleDup != nil {
			fmt.Fprint(l.consoleDup, line)
		}
	}

	if syslogOK {
		l.logToSyslog(level, msg)
	}
}

func (l *Logger) logToSyslog(level Level, msg string) {
	switch level {
	case LevelDebug:
		l.syslogW.Debug(msg)
	case LevelInfo:
		l.syslogW.Info(msg)
	case LevelNotice:
		l.syslogW.Notice(msg)
	case LevelWarn:
		l.syslogW.Warning(msg)
	case LevelError:
		l.syslogW.Err(msg)
	default:
		l.syslogW.Crit(msg)
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
	l.log(LevelInfo, "Service '%s' started", name)
}

// ServiceStopped logs a service stop event.
func (l *Logger) ServiceStopped(name string) {
	l.log(LevelInfo, "Service '%s' stopped", name)
}

// ServiceFailed logs a service failure event.
func (l *Logger) ServiceFailed(name string, depFailed bool) {
	if depFailed {
		l.log(LevelError, "Service '%s' failed to start (dependency failed)", name)
	} else {
		l.log(LevelError, "Service '%s' failed to start", name)
	}
}
