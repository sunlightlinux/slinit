// Package logging implements the slinit logging subsystem.
package logging

import (
	"fmt"
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

// Logger provides structured logging for slinit.
type Logger struct {
	level Level
}

// New creates a new Logger with the specified minimum level.
func New(level Level) *Logger {
	return &Logger{level: level}
}

// SetLevel changes the minimum logging level.
func (l *Logger) SetLevel(level Level) {
	l.level = level
}

func (l *Logger) log(level Level, format string, args ...interface{}) {
	if level < l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("15:04:05")
	fmt.Fprintf(os.Stderr, "[%s] %s: %s\n", timestamp, level, msg)
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
