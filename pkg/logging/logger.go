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
}

// New creates a new Logger with the specified minimum level.
func New(level Level) *Logger {
	return &Logger{level: level, output: os.Stderr, mainLevel: level}
}

// SetOutput redirects log output to the given writer.
func (l *Logger) SetOutput(w io.Writer) {
	l.output = w
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
		timestamp := time.Now().Format("15:04:05")
		fmt.Fprintf(l.output, "[%s] %s: %s\n", timestamp, level, msg)
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
