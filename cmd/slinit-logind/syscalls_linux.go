// Linux-specific syscall wrappers for session lifecycle. Kept in a
// build-tagged file so a future BSD port can supply equivalents
// without editing session.go.
package main

import "golang.org/x/sys/unix"

// nonblock is unix.O_NONBLOCK — imported inline so session.go's fifo
// open doesn't have to reach into unix directly.
const nonblock = unix.O_NONBLOCK

// makeFifo creates a FIFO node at path. Wraps unix.Mkfifo — the
// stdlib doesn't expose one.
func makeFifo(path string, mode uint32) error {
	return unix.Mkfifo(path, mode)
}

// killPID sends the given signal to the process. Broken out so
// session.KillSession's cgroup-procs loop stays platform-portable.
func killPID(pid int, sig int32) error {
	return unix.Kill(pid, unix.Signal(sig))
}
