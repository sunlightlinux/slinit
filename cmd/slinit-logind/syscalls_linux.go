// Linux-specific syscall wrappers for session lifecycle. Kept in a
// build-tagged file so a future BSD port can supply equivalents
// without editing session.go.
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

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

// devPathForMajorMinor returns the /dev path for a character or block
// device identified by major:minor. Used by Session.TakeDevice to
// resolve the compositor's request (typically DRM 226:0 for the
// primary card + input devices) into a filesystem path.
//
// Reads /sys/dev/{char,block}/<major>:<minor>/uevent for the DEVNAME
// entry — the canonical map exposed by udev + kernel.
func devPathForMajorMinor(major, minor uint32) (string, error) {
	for _, kind := range []string{"char", "block"} {
		p := fmt.Sprintf("/sys/dev/%s/%d:%d/uevent", kind, major, minor)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "DEVNAME=") {
				return "/dev/" + strings.TrimPrefix(line, "DEVNAME="), nil
			}
		}
	}
	return "", fmt.Errorf("device %d:%d not found in /sys/dev", major, minor)
}

// openDevice opens a device node read/write with CLOEXEC. Compositors
// need R/W (mutter's DRM master + input event queue).
func openDevice(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|unix.O_CLOEXEC, 0)
}
