//go:build !linux || !cgo

// Package utmp provides utmpx database functions.
// This is a no-op stub for non-Linux platforms or when cgo is disabled.
package utmp

// MaxIDLen is the maximum length of an inittab-id value.
var MaxIDLen = 4

// MaxLineLen is the maximum length of an inittab-line value.
var MaxLineLen = 32

// LogBoot is a no-op on non-Linux platforms.
func LogBoot() bool { return true }

// CreateEntry is a no-op on non-Linux platforms.
func CreateEntry(id, line string, pid int) bool { return true }

// ClearEntry is a no-op on non-Linux platforms.
func ClearEntry(id, line string) {}
